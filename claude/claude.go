package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	Report   string
	Session  string
	Error    error
}

func Run(ctx context.Context, cfg config.Config, taskID string, prompt string, envName string, logFn func(string, ...any)) Result {
	env, ok := cfg.Environments[envName]
	if !ok {
		return Result{ExitCode: 1, Error: fmt.Errorf("environment %q not found", envName)}
	}

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

	// Run pre-hooks
	for _, hook := range env.PreHooks {
		logFn("running pre-hook: %s", hook)
		writeStatus(workDir, ProcessStatus{
			TaskID:    taskID,
			Status:    "running_hook",
			UpdatedAt: time.Now(),
			Message:   hook,
		})
		hookCmd := exec.CommandContext(ctx, "sh", "-c", hook)
		hookDir := env.WorkDir
		if hookDir == "" || hookDir == "." {
			hookDir, _ = os.Getwd()
		}
		hookCmd.Dir = hookDir
		hookCmd.Stdout = os.Stdout
		hookCmd.Stderr = os.Stderr
		if err := hookCmd.Run(); err != nil {
			logFn("pre-hook failed: %s: %v", hook, err)
			return Result{ExitCode: 1, Error: fmt.Errorf("pre-hook %q failed: %w", hook, err)}
		}
	}

	writeStatus(workDir, ProcessStatus{
		TaskID:    taskID,
		Status:    "running",
		UpdatedAt: time.Now(),
	})

	// Run claude code
	claudePath := cfg.Defaults.ClaudeCodePath
	if claudePath == "" {
		claudePath = "claude"
	}

	runDir := env.WorkDir
	if runDir == "" || runDir == "." {
		runDir, _ = os.Getwd()
	}

	args := []string{"--print", "--output-format", "json", prompt}
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = runDir

	// Capture output
	logPath := filepath.Join(workDir, "output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating log file: %w", err)}
	}
	defer logFile.Close()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating stdout pipe: %w", err)}
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		logFn("failed to start claude: %v", err)
		return Result{ExitCode: 1, Error: fmt.Errorf("starting claude: %w", err)}
	}

	logFn("claude process started (pid: %d)", cmd.Process.Pid)

	// Stream output to log file and log function
	var outputBuf []byte
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(logFile, line)
		outputBuf = append(outputBuf, line...)
		outputBuf = append(outputBuf, '\n')
		writeStatus(workDir, ProcessStatus{
			TaskID:    taskID,
			Status:    "running",
			UpdatedAt: time.Now(),
			Message:   truncate(line, 200),
		})
	}

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Parse session ID from JSON output
	sessionID := parseSessionID(outputBuf)

	// Request report via follow-up --resume call if we have a session
	reportText := ""
	if exitCode == 0 && sessionID != "" {
		logFn("requesting report via --resume %s", sessionID)
		reportText = requestReport(ctx, claudePath, runDir, sessionID, logFn)
		// Write report to file for reference
		reportPath := filepath.Join(workDir, "report.md")
		os.WriteFile(reportPath, []byte(reportText), 0644)
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
		Report:   reportText,
		Session:  sessionID,
		Error:    err,
	}
}

// parseSessionID extracts the session_id from Claude's JSON output.
// Claude --output-format json produces a JSON object with a "session_id" field.
func parseSessionID(output []byte) string {
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return ""
	}
	return parsed.SessionID
}

// requestReport runs a follow-up claude invocation with --resume to ask for a report.
func requestReport(ctx context.Context, claudePath string, runDir string, sessionID string, logFn func(string, ...any)) string {
	args := []string{"--print", "--resume", sessionID, "Write a brief summary report (2-4 sentences) of what you just accomplished."}
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = runDir

	out, err := cmd.Output()
	if err != nil {
		logFn("report follow-up failed: %v", err)
		return ""
	}
	return string(out)
}

func writeStatus(workDir string, status ProcessStatus) {
	data, _ := json.MarshalIndent(status, "", "  ")
	os.WriteFile(filepath.Join(workDir, "status.json"), data, 0644)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
