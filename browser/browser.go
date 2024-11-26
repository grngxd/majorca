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
	Ws       interface{} // WebSocket connection, type depends on the browser
	Id       int32
	Pending  map[int32]chan Result
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
		switch ws := b.Ws.(type) {
		case *websocket.Conn:
			if err := ws.Close(); err != nil {
				fmt.Printf("Error closing WebSocket: %v\n", err)
			}
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

// isProcessRunning checks if a process with the given PID is still running.
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, sending signal 0 doesn't work as expected, so we attempt to call Kill with 0.
	err = process.Signal(os.Kill)
	if err != nil {
		return false
	}
	return true
}
