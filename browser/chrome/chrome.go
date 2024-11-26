package chrome

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/grngxd/majorca/browser"
	"golang.org/x/net/websocket"
)

type Chrome struct {
	browser.BaseBrowser
}

func New(args ...string) (*Chrome, error) {
	path, err := FindPath()
	if err != nil {
		return nil, err
	}
	os.Setenv("MAJORCA_BROWSER", path)

	chrome := &Chrome{
		BaseBrowser: browser.BaseBrowser{
			Pending:  make(map[int32]chan browser.Result),
			Bindings: make(map[string]browser.BindingFunc),
			Path:     path,
			Done:     make(chan struct{}), // Initialize done channel
		},
	}

	// Add necessary flags
	args = append(args,
		"--remote-debugging-port=9222", // Standard port
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-popup-blocking",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		"--disable-features=TranslateUI",
		"--app=data:text/html,<!DOCTYPE html><html><head><title>about:blank</title></head><body></body></html>",
		"--disable-features=HoverCard",
	)

	chrome.Cmd = exec.Command(path, args...)
	chrome.Cmd.Stdout = os.Stdout
	chrome.Cmd.Stderr = os.Stderr

	if err := chrome.Start(); err != nil {
		return nil, err
	}

	// Establish the WebSocket connection with retries
	if err := chrome.connectWebSocketWithRetry(10, 1*time.Second); err != nil {
		chrome.Kill()
		return nil, err
	}

	// Start handling responses
	chrome.Wg.Add(1)
	go chrome.handleResponse()

	return chrome, nil
}

// connectWebSocketWithRetry tries to connect to the WebSocket endpoint with retries.
func (c *Chrome) connectWebSocketWithRetry(maxRetries int, delay time.Duration) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = c.connectWebSocket()
		if err == nil {
			return nil
		}
		fmt.Printf("Attempt %d: %v\n", i+1, err)
		time.Sleep(delay)
	}
	return fmt.Errorf("failed to connect to WebSocket after %d attempts: %v", maxRetries, err)
}

// connectWebSocket establishes a WebSocket connection to Chrome's DevTools.
func (c *Chrome) connectWebSocket() error {
	// Check if port is open
	if !waitForPort("localhost", 9222, 10*time.Second) {
		return fmt.Errorf("Chrome remote debugging port 9222 is not open")
	}

	// Fetch the WebSocket debugger URL
	resp, err := http.Get("http://localhost:9222/json")
	if err != nil {
		return fmt.Errorf("failed to get WebSocket debugger URL: %w", err)
	}
	defer resp.Body.Close()

	var targets []struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return fmt.Errorf("failed to decode JSON response: %w", err)
	}

	if len(targets) == 0 {
		return fmt.Errorf("no WebSocket targets found")
	}

	// Connect to the first available WebSocket
	wsURL := targets[0].WebSocketDebuggerURL
	fmt.Printf("Connecting to WebSocket URL: %s\n", wsURL)
	ws, err := websocket.Dial(wsURL, "", "http://localhost")
	if err != nil {
		return fmt.Errorf("failed to dial WebSocket: %w", err)
	}

	fmt.Println("WebSocket connection established")
	c.Ws = ws
	return nil
}

// waitForPort checks if a TCP port is open within a timeout period.
func waitForPort(host string, port int, timeout time.Duration) bool {
	address := fmt.Sprintf("%s:%d", host, port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// handleResponse listens for responses from the WebSocket and dispatches them.
func (c *Chrome) handleResponse() {
	defer c.Wg.Done()
	for {
		select {
		case <-c.Done:
			return
		default:
			var res browser.Result
			if err := websocket.JSON.Receive(c.Ws.(*websocket.Conn), &res); err != nil {
				fmt.Printf("Error receiving response: %v\n", err)
				continue
			}

			c.Lock()
			if ch, ok := c.Pending[res.ID]; ok {
				ch <- res
				delete(c.Pending, res.ID)
			}
			c.Unlock()
		}
	}
}

// Load navigates Chrome to the specified URL.
func (c *Chrome) Load(url string) error {
	c.Lock()
	defer c.Unlock()

	if c.Ws == nil {
		return fmt.Errorf("WebSocket connection is not established")
	}

	message := map[string]interface{}{
		"id":     c.Id,
		"method": "Page.navigate",
		"params": map[string]interface{}{
			"url": url,
		},
	}

	c.Id++
	fmt.Printf("Sending message: %v\n", message)
	if err := websocket.JSON.Send(c.Ws.(*websocket.Conn), message); err != nil {
		return fmt.Errorf("failed to send WebSocket message: %w", err)
	}
	fmt.Println("Page.navigate message sent")
	return nil
}

// Eval evaluates a JavaScript expression in the context of the loaded page.
func (c *Chrome) Eval(expr string) (string, string, error) {
	c.Lock()
	if c.Ws == nil {
		c.Unlock()
		return "", "", fmt.Errorf("WebSocket connection is not established")
	}

	message := map[string]interface{}{
		"id":     c.Id,
		"method": "Runtime.evaluate",
		"params": map[string]interface{}{
			"expression": expr,
		},
	}

	resultChan := make(chan browser.Result)
	c.Pending[c.Id] = resultChan
	c.Id++

	fmt.Printf("Sending message: %v\n", message)
	if err := websocket.JSON.Send(c.Ws.(*websocket.Conn), message); err != nil {
		delete(c.Pending, c.Id-1)
		c.Unlock()
		return "", "", fmt.Errorf("failed to send WebSocket message: %w", err)
	}
	fmt.Println("Runtime.evaluate message sent")
	c.Unlock()

	fmt.Println("Waiting for response")
	res := <-resultChan
	if res.Error != nil {
		return "", "", fmt.Errorf("evaluation error: %s", res.Error.Message)
	}

	fmt.Printf("Received response: %v\n", res)

	// Define a structure to parse the evaluation result
	var evalRes struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}

	// Unmarshal the JSON response into the structure
	if err := json.Unmarshal(res.Result, &evalRes); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return evalRes.Result.Value, evalRes.Result.Type, nil
}

// FindPath locates the Chrome executable path.
func FindPath() (string, error) {
	envPath, _ := os.LookupEnv("MAJORCA_BROWSER")
	if envPath != "" {
		return envPath, nil
	}

	var paths []string

	if runtime.GOOS == "windows" {
		username := os.Getenv("USERNAME")
		paths = []string{
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			filepath.Join("C:\\Users", username, "AppData\\Local\\Google\\Chrome\\Application\\chrome.exe"),

			`C:\Program Files (x86)\Chromium\Application\chrome.exe`,
			`C:\Program Files\Chromium\Application\chrome.exe`,
			filepath.Join("C:\\Users", username, "AppData\\Local\\Chromium\\Application\\chrome.exe"),

			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			filepath.Join("C:\\Users", username, "AppData\\Local\\Microsoft\\Edge\\Application\\msedge.exe"),

			`C:\Program Files (x86)\BraveSoftware\Brave-Browser\Application\brave.exe`,
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
			filepath.Join("C:\\Users", username, "AppData\\Local\\BraveSoftware\\Brave-Browser\\Application\\brave.exe"),
		}
	} else {
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	for _, p := range paths {
		p = os.ExpandEnv(p)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("could not find Chrome binary")
}
