package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighmcculloch/orc/agent"
	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/logging"
	"github.com/leighmcculloch/orc/orchestrator"
	"github.com/leighmcculloch/orc/pick"
	"github.com/leighmcculloch/orc/report"
	"github.com/leighmcculloch/orc/state"
	"github.com/leighmcculloch/orc/tui"
)

func main() {
	cmd := ""
	args := os.Args[1:]

	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "", "run":
		cmdRun(args)
	case "add":
		cmdAdd(args)
	case "list", "ls":
		cmdList(args)
	case "remove", "rm":
		cmdRemove(args)
	case "status":
		cmdStatus(args)
	case "log", "logs":
		cmdLog(args)
	case "report":
		cmdReport(args)
	case "init":
		cmdInit(args)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`orc — AI Agent Orchestrator

Usage:
  orc                                     Start the orchestrator (same as orc run)
  orc run                                 Start the orchestrator (foreground with TUI)
  orc add [-e env] [-s schedule] <prompt>  Add a task
  orc list                                List all tasks
  orc remove <task-id>                    Remove a task
  orc status                              Show task status
  orc log [-d YYYY-MM-DD] [-f] [-t id]    View logs
  orc report [today|yesterday|YYYY-MM-DD]  View completed task reports
  orc init                                Initialize .orc directory with default config

Schedule formats:
  "every 5m"       Run every 5 minutes
  "every 1h"       Run every hour
  "daily 09:00"    Run daily at 09:00
  "hourly"         Run every hour on the hour`)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("orc run", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc run")
		fmt.Fprintln(os.Stderr, "  Start the orchestrator (foreground with TUI)")
	}
	fs.Parse(args)

	if config.IsRunning() {
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
	fs := flag.NewFlagSet("orc add", flag.ExitOnError)
	env := fs.String("e", "", "environment name")
	schedule := fs.String("s", "", "schedule expression (e.g. \"every 5m\", \"daily 09:00\")")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc add [-e env] [-s schedule] <prompt>")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		fs.Usage()
		os.Exit(1)
	}

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

	taskEnv := *env
	if taskEnv == "" {
		taskEnv = cfg.Defaults.Environment
	}

	task := state.Task{
		Prompt:      prompt,
		Environment: taskEnv,
		Schedule:    *schedule,
		Status:      state.TaskPending,
		CreatedAt:   time.Now(),
	}

	task, err = store.AddTask(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Task added: %s\n", task.ID)
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("orc list", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc list")
		fmt.Fprintln(os.Stderr, "  List all tasks")
	}
	fs.Parse(args)

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
		fmt.Printf("%-10s %-12s %-15s %-10s %s\n", t.ID, t.Status, t.Environment, sched, config.Truncate(t.Prompt, 40))
	}
}

func cmdRemove(args []string) {
	fs := flag.NewFlagSet("orc remove", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc remove <task-id>")
		fmt.Fprintln(os.Stderr, "  Remove a task by ID")
	}
	fs.Parse(args)

	taskID := ""
	if fs.NArg() > 0 {
		taskID = fs.Arg(0)
	} else {
		taskID = pickTask("Select task to remove:")
	}
	if taskID == "" {
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

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("orc status", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc status")
		fmt.Fprintln(os.Stderr, "  Show running/pending/completed/failed counts")
	}
	fs.Parse(args)

	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	tasks := store.AllTasks()
	running := 0
	pending := 0
	completed := 0
	failed := 0
	for _, t := range tasks {
		switch t.Status {
		case state.TaskRunning:
			running++
		case state.TaskPending:
			pending++
		case state.TaskCompleted:
			completed++
		case state.TaskFailed, state.TaskCancelled:
			failed++
		}
	}

	if config.IsRunning() {
		fmt.Println("orc is running")
	} else {
		fmt.Println("orc is not running")
	}
	fmt.Printf("  Running:   %d\n", running)
	fmt.Printf("  Pending:   %d\n", pending)
	fmt.Printf("  Completed: %d\n", completed)
	fmt.Printf("  Failed:    %d\n", failed)
}

func cmdLog(args []string) {
	fs := flag.NewFlagSet("orc log", flag.ExitOnError)
	date := fs.String("d", time.Now().Format("2006-01-02"), "date to view logs for (YYYY-MM-DD)")
	follow := fs.Bool("f", false, "follow/stream log output")
	taskID := fs.String("t", "", "task ID to view logs for")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc log [-d YYYY-MM-DD] [-f] [-t task-id]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *taskID != "" || *taskID == "" && isFlagSet(fs, "t") {
		id := *taskID
		if id == "" {
			id = pickTask("Select task to view logs:")
		}
		if id == "" {
			return
		}
		logPath := filepath.Join(config.OrcDir(), "workdirs", id, "output.log")

		if *follow {
			streamFile(logPath)
			return
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "no output log for task %s\n", id)
			os.Exit(1)
		}
		fmt.Print(string(data))
		return
	}

	if *follow {
		ch, cancel, err := logging.StreamLog(*date, true)
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

	lines, err := logging.ReadLog(*date)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

// isFlagSet returns true if the named flag was explicitly set on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func streamFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}

	// Tail the file until interrupted
	for {
		select {
		case <-sig:
			return
		case <-time.After(500 * time.Millisecond):
			for scanner.Scan() {
				fmt.Println(scanner.Text())
			}
		}
	}
}

func cmdReport(args []string) {
	fs := flag.NewFlagSet("orc report", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc report [today|yesterday|YYYY-MM-DD]")
		fmt.Fprintln(os.Stderr, "  View completed task reports for a given date (default: today)")
	}
	fs.Parse(args)

	date := time.Now().Format("2006-01-02")
	if fs.NArg() > 0 {
		switch fs.Arg(0) {
		case "today":
			date = time.Now().Format("2006-01-02")
		case "yesterday":
			date = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
		default:
			date = fs.Arg(0)
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
		fmt.Printf("  Finished: %s\n", entry.FinishedAt.Format("15:04:05"))
	}
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("orc init", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: orc init")
		fmt.Fprintln(os.Stderr, "  Initialize .orc directory with default config")
	}
	fs.Parse(args)

	if err := config.EnsureOrcDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg := config.DefaultConfig()
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Create empty jobs files
	store, err := state.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := store.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Write orc-add helper script
	if err := agent.WriteOrcAddScript(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Initialized .orc directory")
	fmt.Println("  Config: .orc/config.json")
	fmt.Println("  Jobs:   .orc/jobs/")
	fmt.Println()
	fmt.Println("Edit .orc/config.json to configure environments and settings.")
	fmt.Println("Run 'orc run' to start the orchestrator.")
}

func loadTasks() []state.Task {
	store, err := state.Load()
	if err != nil {
		return nil
	}
	return store.AllTasks()
}

func tasksToItems(tasks []state.Task) []pick.Item {
	items := make([]pick.Item, len(tasks))
	for i, t := range tasks {
		sched := ""
		if t.Schedule != "" {
			sched = " [" + t.Schedule + "]"
		}
		items[i] = pick.Item{
			ID:    t.ID,
			Label: fmt.Sprintf("%-10s %-12s %s%s", t.ID, t.Status, config.Truncate(t.Prompt, 50), sched),
		}
	}
	return items
}

func pickTask(title string) string {
	tasks := loadTasks()
	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return ""
	}
	item, ok := pick.Run(title, tasksToItems(tasks))
	if !ok {
		return ""
	}
	return item.ID
}
