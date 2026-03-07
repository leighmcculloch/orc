package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leighmcculloch/orc/config"
)

type ProcessStatus struct {
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Message   string    `json:"message,omitempty"`
}

type Result struct {
	ExitCode int
	Error    error
}

func Run(ctx context.Context, cfg config.Config, taskID string, prompt string, logFn func(string, ...any)) Result {
	workDir := filepath.Join(config.OrcDir(), "workdirs", taskID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating work dir: %w", err)}
	}

	// Write status file
	writeStatus(workDir, ProcessStatus{
		TaskID:    taskID,
		Status:    "starting",
		UpdatedAt: time.Now(),
	})

	writeStatus(workDir, ProcessStatus{
		TaskID:    taskID,
		Status:    "running",
		UpdatedAt: time.Now(),
	})

	agentCmd := cfg.Defaults.Command
	if agentCmd == "" {
		return Result{ExitCode: 1, Error: fmt.Errorf("no command configured; set \"command\" in %s", config.ConfigPath())}
	}

	// Append orc instructions to the prompt
	absInbox, _ := filepath.Abs(filepath.Join(config.OrcDir(), "jobs", "inbox"))
	fullPrompt := prompt + "\n\n" + orcInstructions(absInbox)

	// Write prompt to a file so it can be safely passed to the shell command
	promptPath := filepath.Join(workDir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(fullPrompt), 0644); err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("writing prompt file: %w", err)}
	}

	// Replace $prompt with a shell command that reads the prompt file
	shellCmd := strings.Replace(agentCmd, "$prompt", "$(cat "+shellQuote(promptPath)+")", 1)

	// Write the final command for debugging
	os.WriteFile(filepath.Join(workDir, "command.txt"), []byte(shellCmd), 0644)

	// Capture output to log file
	logPath := filepath.Join(workDir, "output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating log file: %w", err)}
	}
	defer logFile.Close()

	// Parse shutdown grace period
	graceDuration := 10 * time.Second
	if cfg.Defaults.ShutdownGrace != "" {
		if d, err := time.ParseDuration(cfg.Defaults.ShutdownGrace); err == nil {
			graceDuration = d
		}
	}

	// Use exec.Command (not CommandContext) so we can handle signals ourselves
	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Dir = workDir
	// Set process group so we can signal the entire group
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating stdout pipe: %w", err)}
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		logFn("failed to start agent: %v", err)
		return Result{ExitCode: 1, Error: fmt.Errorf("starting agent: %w", err)}
	}

	logFn("agent process started (pid: %d)", cmd.Process.Pid)

	// Watch for context cancellation and handle graceful shutdown
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Context cancelled — attempt graceful shutdown
			writeStatus(workDir, ProcessStatus{
				TaskID:    taskID,
				Status:    "stopping",
				UpdatedAt: time.Now(),
				Message:   "sending SIGTERM",
			})
			logFn("sending SIGTERM to process group (pid: %d)", cmd.Process.Pid)
			// Signal the entire process group
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

			// Wait for grace period or process exit
			select {
			case <-doneCh:
				// Process exited within grace period
				return
			case <-time.After(graceDuration):
				// Grace period expired, force kill
				logFn("grace period expired, sending SIGKILL (pid: %d)", cmd.Process.Pid)
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-doneCh:
			// Process exited normally, no need to signal
		}
	}()

	// Stream output to log file
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(logFile, line)
		writeStatus(workDir, ProcessStatus{
			TaskID:    taskID,
			Status:    "running",
			UpdatedAt: time.Now(),
			Message:   config.Truncate(line, 200),
		})
	}

	err = cmd.Wait()
	close(doneCh)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Context cancelled
	if ctx.Err() != nil {
		writeStatus(workDir, ProcessStatus{
			TaskID:    taskID,
			Status:    "cancelled",
			UpdatedAt: time.Now(),
		})
		return Result{ExitCode: 1, Error: ctx.Err()}
	}

	status := "completed"
	if exitCode != 0 {
		status = "failed"
	}
	writeStatus(workDir, ProcessStatus{
		TaskID:    taskID,
		Status:    status,
		UpdatedAt: time.Now(),
	})

	return Result{
		ExitCode: exitCode,
		Error:    err,
	}
}

// orcInstructions returns instructions appended to every agent prompt explaining
// how to create subtasks by writing prompt files to the inbox directory.
func orcInstructions(inboxDir string) string {
	return fmt.Sprintf(`--- ORC INSTRUCTIONS ---
You are running as a task inside orc, a task orchestrator.
To create a subtask for another agent, write the prompt to a .txt file in the inbox:

  echo "your task prompt" > %s/$(date +%%s)-$RANDOM.txt

Use subtasks only for independent work that can be done in parallel.
Do not create subtasks for work you can do yourself.

You have limited permissions: you cannot push to remotes, open PRs, or write
to repositories outside your working directory. Any actions requiring those
must be done by the user.

When you complete work that requires user action (pushing commits, opening a PR,
reviewing changes, etc.), write a NOTES.md file in your working directory containing:
- A brief summary of what you did (first line)
- Any commands the user should run next

The user will see these notes in the orc dashboard.`, inboxDir)
}

func writeStatus(workDir string, status ProcessStatus) {
	data, _ := json.MarshalIndent(status, "", "  ")
	os.WriteFile(filepath.Join(workDir, "status.json"), data, 0644)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
