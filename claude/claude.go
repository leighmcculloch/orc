package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Ensure orc-add helper script exists
	if err := WriteOrcAddScript(); err != nil {
		logFn("failed to write orc-add script: %v", err)
		return Result{ExitCode: 1, Error: fmt.Errorf("writing orc-add script: %w", err)}
	}

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

	// Run agent command
	runDir := env.WorkDir
	if runDir == "" || runDir == "." {
		runDir, _ = os.Getwd()
	}

	agentCmd := cfg.Defaults.AgentCommand
	if agentCmd == "" {
		agentCmd = config.DefaultAgentCommand
	}

	// Append orc instructions to the prompt
	fullPrompt := prompt + "\n\n" + orcInstructions()

	// Write prompt to a file so it can be safely passed to the shell command
	// without shell escaping issues. The agent command template uses $prompt
	// which we replace with a shell-safe reference to the file.
	promptPath := filepath.Join(workDir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(fullPrompt), 0644); err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("writing prompt file: %w", err)}
	}

	// Replace $prompt with a shell command that reads the prompt file
	shellCmd := strings.Replace(agentCmd, "$prompt", "$(cat "+shellQuote(promptPath)+")", 1)

	// Capture output to log file
	logPath := filepath.Join(workDir, "output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{ExitCode: 1, Error: fmt.Errorf("creating log file: %w", err)}
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd)
	cmd.Dir = runDir

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
// how to create subtasks via the orc-add helper script.
func orcInstructions() string {
	return `--- ORC INSTRUCTIONS ---
You are running as a task inside orc, a task orchestrator.
You can create new tasks for other agents to work on by running:

  .orc/bin/orc-add "your task prompt here"

This will submit the task to orc's queue and it will be picked up by another agent.
Use this when a subtask is independent and can be done in parallel.
Do not create subtasks for work you can do yourself in the current session.`
}

// writeOrcAddScript writes the orc-add helper script to .orc/bin/orc-add.
// The script writes a JSON command file to .orc/inbox/ to add a task via IPC.
func WriteOrcAddScript() error {
	binDir := filepath.Join(config.OrcDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	// Resolve absolute path to inbox so the script works from any working directory
	absInbox, err := filepath.Abs(filepath.Join(config.OrcDir(), "inbox"))
	if err != nil {
		return fmt.Errorf("resolving inbox path: %w", err)
	}

	script := fmt.Sprintf(`#!/bin/sh
set -e
prompt="$*"
if [ -z "$prompt" ]; then
  echo "usage: orc-add <prompt>" >&2
  exit 1
fi
id=$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')
inbox=%s
mkdir -p "$inbox"
# Escape JSON special characters in prompt
escaped=$(printf '%%s' "$prompt" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | awk '{if(NR>1) printf "\\n"; printf "%%s",$0}')
tmp="$inbox/$id.json.tmp"
cat > "$tmp" <<JSONEOF
{
  "id": "$id",
  "command": "add_task",
  "payload": {"prompt": "$escaped"}
}
JSONEOF
mv "$tmp" "$inbox/$id.json"
echo "task submitted: $id"
`, shellQuote(absInbox))
	scriptPath := filepath.Join(binDir, "orc-add")
	return os.WriteFile(scriptPath, []byte(script), 0755)
}

func writeStatus(workDir string, status ProcessStatus) {
	data, _ := json.MarshalIndent(status, "", "  ")
	os.WriteFile(filepath.Join(workDir, "status.json"), data, 0644)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
