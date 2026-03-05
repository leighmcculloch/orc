package ipc

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/leighmcculloch/orc/config"
)

type CommandType string

const (
	CmdAddTask    CommandType = "add_task"
	CmdListTasks  CommandType = "list_tasks"
	CmdRemoveTask CommandType = "remove_task"
	CmdGetStatus  CommandType = "get_status"
	CmdStop       CommandType = "stop"
)

type Request struct {
	ID      string          `json:"id"`
	Command CommandType     `json:"command"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type AddTaskPayload struct {
	Prompt      string `json:"prompt"`
	Environment string `json:"environment"`
	Schedule    string `json:"schedule,omitempty"`
}

type RemoveTaskPayload struct {
	TaskID string `json:"task_id"`
}

func InboxDir() string {
	return filepath.Join(config.OrcDir(), "inbox")
}

func OutboxDir() string {
	return filepath.Join(config.OrcDir(), "outbox")
}

func PidPath() string {
	return filepath.Join(config.OrcDir(), "orc.pid")
}

func EnsureIPCDirs() error {
	for _, d := range []string{InboxDir(), OutboxDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}

// IsRunning checks if orc is running by reading the pid file and verifying
// the process is alive. Removes stale pid files.
func IsRunning() bool {
	data, err := os.ReadFile(PidPath())
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		os.Remove(PidPath())
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(PidPath())
		return false
	}
	// Signal 0 checks if process exists without actually sending a signal
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(PidPath())
		return false
	}
	return true
}

// WritePid writes the current process pid to the pid file.
func WritePid() error {
	return os.WriteFile(PidPath(), []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
}

// RemovePid removes the pid file.
func RemovePid() {
	os.Remove(PidPath())
}

// SendCommand writes a request to the inbox and waits for a response in the outbox.
func SendCommand(req Request) (Response, error) {
	if err := EnsureIPCDirs(); err != nil {
		return Response{}, err
	}

	// Generate a request ID
	req.ID = randomID()

	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	// Write to inbox atomically (write tmp, rename)
	inboxFile := filepath.Join(InboxDir(), req.ID+".json")
	tmp := inboxFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return Response{}, fmt.Errorf("writing request: %w", err)
	}
	if err := os.Rename(tmp, inboxFile); err != nil {
		return Response{}, fmt.Errorf("moving request to inbox: %w", err)
	}

	// Poll outbox for response
	outboxFile := filepath.Join(OutboxDir(), req.ID+".json")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		respData, err := os.ReadFile(outboxFile)
		if err == nil {
			os.Remove(outboxFile)
			var resp Response
			if err := json.Unmarshal(respData, &resp); err != nil {
				return Response{}, fmt.Errorf("parsing response: %w", err)
			}
			return resp, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Timeout — clean up inbox file if still there
	os.Remove(inboxFile)
	return Response{}, fmt.Errorf("timeout waiting for response (is orc running?)")
}

// PollInbox reads and removes all pending requests from the inbox.
func PollInbox() ([]Request, error) {
	entries, err := os.ReadDir(InboxDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var requests []Request
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(InboxDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			os.Remove(path) // malformed, discard
			continue
		}
		os.Remove(path) // consumed
		requests = append(requests, req)
	}
	return requests, nil
}

// WriteResponse writes a response to the outbox for a given request ID.
func WriteResponse(requestID string, resp Response) error {
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	outboxFile := filepath.Join(OutboxDir(), requestID+".json")
	tmp := outboxFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, outboxFile)
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
