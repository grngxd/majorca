package firefox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/grngxd/majorca/browser"
	"golang.org/x/net/websocket"
)

type Firefox struct {
	browser.BaseBrowser
	Id      int32
	mu      sync.Mutex
	profile string
}

func New(args ...string) (*Firefox, error) {
	path, err := FindPath()
	if err != nil {
		return nil, err
	}
	os.Setenv("MAJORCA_BROWSER", path)

	profileDir := filepath.Join(os.TempDir(), fmt.Sprintf("firefox_profile_%d", time.Now().UnixNano()))

	firefox := &Firefox{
		BaseBrowser: browser.BaseBrowser{
			Pending:  make(map[string]chan interface{}),
			Bindings: make(map[string]browser.BindingFunc),
			Path:     path,
			Done:     make(chan struct{}),
		},
		Id:      1,
		profile: profileDir,
	}

	err = os.MkdirAll(profileDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firefox profile directory: %w", err)
	}
	fmt.Printf("Profile directory: %s\n", profileDir)

	if err := customizeProfile(profileDir); err != nil {
		return nil, fmt.Errorf("failed to customize Firefox profile: %w", err)
	}

	// Add necessary flags
	args = append(args,
		"--remote-debugging-port=9223",
		"--no-remote",
		"--profile", profileDir,
		"--new-instance",
		"--start-debugger-server",
		"--no-extensions",
		"--disable-popup-blocking",
		"--disable-infobars",
		"about:blank",
	)

	firefox.Cmd = exec.Command(path, args...)
	firefox.Cmd.Stdout = os.Stdout
	firefox.Cmd.Stderr = os.Stderr

	if err := firefox.Start(); err != nil {
		return nil, err
	}

	if err := firefox.connectWebSocketWithRetry(10, 1*time.Second); err != nil {
		firefox.Kill()
		return nil, err
	}

	firefox.Wg.Add(1)
	go firefox.handleResponse()

	return firefox, nil
}

// the profile dir is like 100mb give or take a bit so we gotta delete it
func customizeProfile(profileDir string) error {
	userJSPath := filepath.Join(profileDir, "user.js")
	userJSContent := []byte(
		`user_pref("toolkit.legacyUserProfileCustomizations.stylesheets", true);
		user_pref("browser.tabs.drawInTitlebar", true);
		user_pref("browser.tabs.inTitlebar", 0);
		user_pref("devtools.policy.disabled", true);`,
	)
	err := os.WriteFile(userJSPath, userJSContent, 0644)
	if err != nil {
		return fmt.Errorf("failed to write user.js: %w", err)
	}

	// Create chrome directory
	chromeDir := filepath.Join(profileDir, "chrome")
	err = os.MkdirAll(chromeDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create chrome directory: %w", err)
	}

	// Create userChrome.css to hide UI elements except the title bar
	userChromeCSSPath := filepath.Join(chromeDir, "userChrome.css")
	userChromeCSSContent := []byte(
		`/* Hide the tab bar */
#TabsToolbar { visibility: collapse !important; }

/* Hide the navigation toolbar */
#nav-bar { visibility: collapse !important; }

/* Hide the bookmarks toolbar */
#PersonalToolbar { visibility: collapse !important; }

/* Hide the address bar */
#urlbar-container { visibility: collapse !important; }

/* Hide the status bar */
#status-bar { visibility: collapse !important; }

/* Optional: Remove padding around the content */
.vbox {
    padding: 0 !important;
}`,
	)
	err = os.WriteFile(userChromeCSSPath, userChromeCSSContent, 0644)
	if err != nil {
		return fmt.Errorf("failed to write userChrome.css: %w", err)
	}

	return nil
}

// override Kill method so that we can delete the profile
func (f *Firefox) Kill() error {
	// call super class Kill method
	err := f.BaseBrowser.Kill()
	if err != nil {
		return err
	}

	// delete profile directory
	err = os.RemoveAll(f.profile)
	if err != nil {
		return fmt.Errorf("failed to delete Firefox profile directory: %w", err)
	}

	return nil
}

// connectWebSocketWithRetry tries to connect to the WebSocket endpoint with retries.
func (f *Firefox) connectWebSocketWithRetry(maxRetries int, delay time.Duration) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = f.connectWebSocket()
		if err == nil {
			return nil
		}
		fmt.Printf("Attempt %d: %v\n", i+1, err)
		time.Sleep(delay)
	}
	return fmt.Errorf("failed to connect to WebSocket after %d attempts: %v", maxRetries, err)
}

// connectWebSocket establishes a WebSocket connection to Firefox's Remote Debugging.
func (f *Firefox) connectWebSocket() error {
	// Check if port is open
	if !waitForPort("localhost", 9223, 10*time.Second) {
		return fmt.Errorf("Firefox remote debugging port 9223 is not open")
	}

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
func (f *Firefox) handleResponse() {
	defer f.Wg.Done()
	for {
		select {
		case <-f.Done:
			return
		default:
			var res browser.Result
			if err := websocket.JSON.Receive(f.Ws, &res); err != nil {
				fmt.Printf("Error receiving response: %v\n", err)
				continue
			}

			idStr := fmt.Sprintf("%d", res.ID)
			f.Lock()
			if ch, ok := f.Pending[idStr]; ok {
				ch <- res
				delete(f.Pending, idStr)
			}
			f.Unlock()
		}
	}
}

// Load navigates Firefox to the specified URL.
func (f *Firefox) Load(url string) error {
	return nil
}

// Eval evaluates a JavaScript expression in the context of the loaded page.
func (f *Firefox) Eval(expr string) (string, string, error) {
	return "", "", nil
}

// FindPath locates the Firefox executable path.
func FindPath() (string, error) {
	envPath, _ := os.LookupEnv("MAJORCA_BROWSER")
	if envPath != "" {
		return envPath, nil
	}

	var paths []string

	if runtime.GOOS == "windows" {
		username := os.Getenv("USERNAME")
		paths = []string{
			`C:\Program Files\Mozilla Firefox\firefox.exe`,
			`C:\Program Files (x86)\Mozilla Firefox\firefox.exe`,
			filepath.Join("C:\\Users", username, "AppData\\Local\\Mozilla Firefox\\firefox.exe"),
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

	return "", fmt.Errorf("could not find Firefox binary")
}
