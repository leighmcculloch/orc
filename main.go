package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/ipc"
	"github.com/leighmcculloch/orc/logging"
	"github.com/leighmcculloch/orc/orchestrator"
	"github.com/leighmcculloch/orc/report"
	"github.com/leighmcculloch/orc/state"
	"github.com/leighmcculloch/orc/tui"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		cmdRun()
	case "add":
		cmdAdd(args)
	case "list", "ls":
		cmdList()
	case "remove", "rm":
		cmdRemove(args)
	case "status":
		cmdStatus()
	case "log", "logs":
		cmdLog(args)
	case "report":
		cmdReport(args)
	case "init":
		cmdInit()
	case "stop":
		cmdStop()
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`orc — Claude Code Orchestrator

Usage:
  orc run                      Start the orchestrator (foreground with TUI)
  orc add <prompt>             Add an ad-hoc task
  orc add -e <env> <prompt>    Add a task with a specific environment
  orc add -s <schedule> <prompt>  Add a scheduled task
  orc list                     List all tasks
  orc remove <task-id>         Remove a task
  orc status                   Show orchestrator status
  orc log [--date YYYY-MM-DD] [--follow]  View logs
  orc report [today|yesterday|YYYY-MM-DD]  View completed task reports
  orc init                     Initialize .orc directory with default config
  orc stop                     Stop the running orchestrator

Schedule formats:
  "every 5m"       Run every 5 minutes
  "every 1h"       Run every hour
  "daily 09:00"    Run daily at 09:00
  "hourly"         Run every hour on the hour`)
}

func cmdRun() {
	if ipc.IsRunning() {
		fmt.Fprintln(os.Stderr, "orc is already running")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading config: %v\n", err)
		os.Exit(1)
	}

	if err := config.EnsureOrcDir(); err != nil {
		fmt.Fprintf(os.Stderr, "creating .orc directory: %v\n", err)
		os.Exit(1)
	}

	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading state: %v\n", err)
		os.Exit(1)
	}

	logger, err := logging.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	orc, err := orchestrator.New(cfg, store, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating orchestrator: %v\n", err)
		os.Exit(1)
	}

	// Run orchestrator in background
	go func() {
		if err := orc.Run(); err != nil {
			logger.Log("orchestrator error: %v", err)
		}
	}()

	// Run TUI in foreground
	if err := tui.Run(orc); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}

func cmdAdd(args []string) {
	env := ""
	schedule := ""
	var promptParts []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-e", "--env":
			if i+1 < len(args) {
				env = args[i+1]
				i++
			}
		case "-s", "--schedule":
			if i+1 < len(args) {
				schedule = args[i+1]
				i++
			}
		default:
			promptParts = append(promptParts, args[i])
		}
	}

	prompt := strings.Join(promptParts, " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: orc add [-e env] [-s schedule] <prompt>")
		os.Exit(1)
	}

	payload := ipc.AddTaskPayload{
		Prompt:      prompt,
		Environment: env,
		Schedule:    schedule,
	}

	// If orc is running, send via inbox file
	if ipc.IsRunning() {
		data, _ := json.Marshal(payload)
		resp, err := ipc.SendCommand(ipc.Request{
			Command: ipc.CmdAddTask,
			Payload: data,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !resp.OK {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		var task state.Task
		json.Unmarshal(resp.Payload, &task)
		fmt.Printf("Task added: %s\n", task.ID)
		return
	}

	// If not running, add directly to state file
	if err := config.EnsureOrcDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	taskEnv := payload.Environment
	if taskEnv == "" {
		taskEnv = cfg.Defaults.Environment
	}

	task := state.Task{
		ID:          generateID(),
		Prompt:      prompt,
		Environment: taskEnv,
		Schedule:    schedule,
		Status:      state.TaskPending,
		CreatedAt:   time.Now(),
	}

	if err := store.AddTask(task); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Task added: %s (orc not running — task will start when orc runs)\n", task.ID)
}

func cmdList() {
	if ipc.IsRunning() {
		resp, err := ipc.SendCommand(ipc.Request{Command: ipc.CmdListTasks})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !resp.OK {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		var tasks []state.Task
		json.Unmarshal(resp.Payload, &tasks)
		printTasks(tasks)
		return
	}

	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printTasks(store.AllTasks())
}

func printTasks(tasks []state.Task) {
	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return
	}
	fmt.Printf("%-10s %-12s %-15s %-10s %s\n", "ID", "STATUS", "ENVIRONMENT", "SCHEDULE", "PROMPT")
	fmt.Println(strings.Repeat("-", 80))
	for _, t := range tasks {
		sched := t.Schedule
		if sched == "" {
			sched = "-"
		}
		fmt.Printf("%-10s %-12s %-15s %-10s %s\n", t.ID, t.Status, t.Environment, sched, truncate(t.Prompt, 40))
	}
}

func cmdRemove(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: orc remove <task-id>")
		os.Exit(1)
	}
	taskID := args[0]

	if ipc.IsRunning() {
		payload := ipc.RemoveTaskPayload{TaskID: taskID}
		data, _ := json.Marshal(payload)
		resp, err := ipc.SendCommand(ipc.Request{
			Command: ipc.CmdRemoveTask,
			Payload: data,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !resp.OK {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		fmt.Printf("Task removed: %s\n", taskID)
		return
	}

	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := store.RemoveTask(taskID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task removed: %s\n", taskID)
}

func cmdStatus() {
	if !ipc.IsRunning() {
		fmt.Println("orc is not running")

		// Still show state file info
		store, err := state.Load()
		if err != nil {
			return
		}
		tasks := store.AllTasks()
		if len(tasks) > 0 {
			fmt.Printf("\nQueued tasks: %d\n", len(tasks))
			printTasks(tasks)
		}
		return
	}

	resp, err := ipc.SendCommand(ipc.Request{Command: ipc.CmdGetStatus})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var status struct {
		Running   int          `json:"running"`
		Pending   int          `json:"pending"`
		Completed int          `json:"completed"`
		Failed    int          `json:"failed"`
		Tasks     []state.Task `json:"tasks"`
	}
	json.Unmarshal(resp.Payload, &status)

	fmt.Println("orc is running")
	fmt.Printf("  Running:   %d\n", status.Running)
	fmt.Printf("  Pending:   %d\n", status.Pending)
	fmt.Printf("  Completed: %d\n", status.Completed)
	fmt.Printf("  Failed:    %d\n", status.Failed)
}

func cmdLog(args []string) {
	date := time.Now().Format("2006-01-02")
	follow := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--date", "-d":
			if i+1 < len(args) {
				date = args[i+1]
				i++
			}
		case "--follow", "-f":
			follow = true
		case "--task", "-t":
			if i+1 < len(args) {
				taskID := args[i+1]
				logPath := logging.TaskLogPath(taskID)
				data, err := os.ReadFile(logPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error reading task log: %v\n", err)
					os.Exit(1)
				}
				fmt.Print(string(data))
				return
			}
		}
	}

	if follow {
		ch, cancel, err := logging.StreamLog(date, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer cancel()
		for line := range ch {
			fmt.Println(line)
		}
		return
	}

	lines, err := logging.ReadLog(date)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

func cmdReport(args []string) {
	date := time.Now().Format("2006-01-02")
	if len(args) > 0 {
		switch args[0] {
		case "today":
			date = time.Now().Format("2006-01-02")
		case "yesterday":
			date = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
		default:
			date = args[0]
		}
	}

	r, err := report.GetReport(date)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(r.Entries) == 0 {
		fmt.Printf("No completed tasks for %s\n", date)
		return
	}

	fmt.Printf("Tasks completed on %s:\n", date)
	fmt.Println(strings.Repeat("-", 60))
	for _, entry := range r.Entries {
		fmt.Printf("\n[%s] %s\n", entry.TaskID, entry.Prompt)
		fmt.Printf("  Status: %s\n", entry.Status)
		if entry.Report != "" {
			fmt.Printf("  Report: %s\n", entry.Report)
		}
		fmt.Printf("  Finished: %s\n", entry.FinishedAt.Format("15:04:05"))
	}
}

func cmdInit() {
	if err := config.EnsureOrcDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg := config.DefaultConfig()
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Create empty state
	store := &state.Store{Tasks: []state.Task{}}
	if err := store.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Initialized .orc directory")
	fmt.Println("  Config: .orc/config.json")
	fmt.Println("  State:  .orc/state.json")
	fmt.Println()
	fmt.Println("Edit .orc/config.json to configure environments and settings.")
	fmt.Println("Run 'orc run' to start the orchestrator.")
}

func cmdStop() {
	if !ipc.IsRunning() {
		fmt.Println("orc is not running")
		return
	}
	resp, err := ipc.SendCommand(ipc.Request{Command: ipc.CmdStop})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if resp.OK {
		fmt.Println("orc stop signal sent")
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func generateID() string {
	b := make([]byte, 4)
	t := time.Now().UnixNano()
	b[0] = byte(t >> 24)
	b[1] = byte(t >> 16)
	b[2] = byte(t >> 8)
	b[3] = byte(t)
	return fmt.Sprintf("%x", b)
}
