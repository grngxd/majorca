package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/net/websocket"
)

type Browser interface {
	Start() error
	Kill() error
	Eval(expr string) (string, string, error)
	Bind(name string, f BindingFunc) error
	Load(url string) error
}

type BindingFunc func(args []json.RawMessage) (interface{}, error)

type Result struct {
	ID     int32           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type BaseBrowser struct {
	sync.Mutex
	Path     string
	Cmd      *exec.Cmd
	Ws       *websocket.Conn // Changed from *websocket.Conn (gorilla) to *websocket.Conn (golang/x/net)
	Id       int32
	Pending  map[string]chan interface{}
	Bindings map[string]BindingFunc
	Done     chan struct{}  // Channel to signal goroutine to stop
	Wg       sync.WaitGroup // WaitGroup to wait for goroutines to finish
}

func (b *BaseBrowser) Start() error {
	b.Lock()
	defer b.Unlock()

	if b.Cmd.Process != nil {
		fmt.Println("Browser process already started")
		return nil
	}

	if err := b.Cmd.Start(); err != nil {
		return fmt.Errorf("failed to start browser: %w", err)
	}

	fmt.Println("Browser started successfully")
	return nil
}

func (b *BaseBrowser) Kill() error {
	b.Lock()
	defer b.Unlock()

	// Signal handleResponse to stop
	select {
	case <-b.Done:
		// done channel already closed
	default:
		close(b.Done)
	}

	if b.Ws != nil {
		// Close WebSocket connection if applicable
		if err := b.Ws.Close(); err != nil {
			fmt.Printf("Error closing WebSocket: %v\n", err)
		}
	}

	// Wait for handleResponse goroutine to finish
	b.Wg.Wait()

	if b.Cmd.Process != nil {
		if err := b.Cmd.Process.Kill(); err != nil {
			// On Windows, TerminateProcess can fail if the process is already terminated.
			// Therefore, check if the process is still running before returning an error.
			if !isProcessRunning(b.Cmd.Process.Pid) {
				return nil
			}
			return fmt.Errorf("failed to kill browser process: %w", err)
		}
	}

	return nil
}

func (b *BaseBrowser) Eval(expr string) (string, string, error) {
	// This method should be implemented by specific browsers
	return "", "", fmt.Errorf("Eval not implemented")
}

func (b *BaseBrowser) Load(url string) error {
	// This method should be implemented by specific browsers
	return fmt.Errorf("Load not implemented")
}

func (b *BaseBrowser) Bind(name string, f BindingFunc) error {
	b.Lock()
	defer b.Unlock()
	if _, exists := b.Bindings[name]; exists {
		return fmt.Errorf("binding %s already exists", name)
	}
	b.Bindings[name] = f
	return nil
}

// isProcessRunning checks if a process with the given PID is still running.
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, sending signal os.Kill doesn't work as expected, so we attempt to call Kill with os.Kill.
	err = process.Signal(os.Kill)
	if err != nil {
		return false
	}
	return true
}
