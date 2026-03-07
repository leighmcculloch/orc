package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/logging"
	"github.com/leighmcculloch/orc/orchestrator"
	"github.com/leighmcculloch/orc/pick"
	"github.com/leighmcculloch/orc/report"
	"github.com/leighmcculloch/orc/state"
	"github.com/leighmcculloch/orc/tui"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:           "orc",
		Short:         "AI Agent Orchestrator",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddGroup(
		&cobra.Group{ID: "orchestrator", Title: "Orchestrator:"},
		&cobra.Group{ID: "tasks", Title: "Tasks:"},
	)

	run := runCmd()
	run.GroupID = "orchestrator"
	status := statusCmd()
	status.GroupID = "orchestrator"
	log := logCmd()
	log.GroupID = "orchestrator"

	add := addCmd()
	add.GroupID = "tasks"
	ls := listCmd()
	ls.GroupID = "tasks"
	rm := removeCmd()
	rm.GroupID = "tasks"
	rpt := reportCmd()
	rpt.GroupID = "tasks"

	rootCmd.AddCommand(run, status, log, add, ls, rm, rpt)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func ensureInitialized() error {
	if err := config.EnsureOrcDir(); err != nil {
		return err
	}
	if _, err := os.Stat(config.ConfigPath()); os.IsNotExist(err) {
		if err := config.Save(config.DefaultConfig()); err != nil {
			return err
		}
	}
	store, err := state.Load()
	if err != nil {
		return err
	}
	return store.Save()
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the orchestrator (foreground with TUI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrchestrator()
		},
	}
}

func runOrchestrator() error {
	if config.IsRunning() {
		return fmt.Errorf("orc is already running\n\n  The orchestrator appears to be running (pid file: %s).\n  If it crashed, remove the pid file and try again.", config.PidPath())
	}

	if err := ensureInitialized(); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if cfg.Defaults.Command == "" {
		return fmt.Errorf("no command configured\n\n  Set \"command\" in %s, e.g.:\n\n  {\n    \"defaults\": {\n      \"command\": \"claude -p \\\"$prompt\\\" --dangerously-skip-permissions\"\n    }\n  }", config.ConfigPath())
	}

	store, err := state.Load()
	if err != nil {
		return err
	}

	logger, err := logging.New()
	if err != nil {
		return err
	}
	defer logger.Close()

	orc, err := orchestrator.New(cfg, store, logger)
	if err != nil {
		return err
	}

	go func() {
		if err := orc.Run(); err != nil {
			logger.Log("orchestrator error: %v", err)
		}
	}()

	return tui.Run(orc)
}

func statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show orchestrator status overview",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return showStatus(jsonOutput)
		},
	}
	cmd.Flags().Bool("json", false, "output in JSON format")
	return cmd
}

func showStatus(jsonOutput bool) error {
	store, err := state.Load()
	if err != nil {
		return err
	}

	tasks := store.AllTasks()

	// Count tasks by status
	counts := map[state.TaskStatus]int{}
	var running, pending, recentFailed []state.Task
	for _, t := range tasks {
		counts[t.Status]++
		switch t.Status {
		case state.TaskRunning:
			running = append(running, t)
		case state.TaskPending:
			pending = append(pending, t)
		case state.TaskFailed:
			recentFailed = append(recentFailed, t)
		}
	}

	// Limit recent failures to last 5
	if len(recentFailed) > 5 {
		recentFailed = recentFailed[len(recentFailed)-5:]
	}

	pid, isRunning := config.RunningPid()

	if jsonOutput {
		type jsonTask struct {
			ID       string `json:"id"`
			Prompt   string `json:"prompt"`
			Schedule string `json:"schedule,omitempty"`
			Elapsed  string `json:"elapsed,omitempty"`
			Error    string `json:"error,omitempty"`
		}
		type jsonOutput struct {
			Running bool           `json:"running"`
			PID     int            `json:"pid,omitempty"`
			Tasks   map[string]int `json:"tasks"`
			RunList []jsonTask     `json:"running_tasks"`
			Pending []jsonTask     `json:"pending_tasks"`
			Failed  []jsonTask     `json:"recent_failures"`
		}
		out := jsonOutput{
			Running: isRunning,
			Tasks: map[string]int{
				"running":   counts[state.TaskRunning],
				"pending":   counts[state.TaskPending],
				"completed": counts[state.TaskCompleted],
				"failed":    counts[state.TaskFailed],
				"cancelled": counts[state.TaskCancelled],
			},
		}
		if isRunning {
			out.PID = pid
		}
		for _, t := range running {
			jt := jsonTask{ID: t.ID, Prompt: t.Prompt, Schedule: t.Schedule}
			if t.StartedAt != nil {
				jt.Elapsed = time.Since(*t.StartedAt).Truncate(time.Second).String()
			}
			out.RunList = append(out.RunList, jt)
		}
		for _, t := range pending {
			jt := jsonTask{ID: t.ID, Prompt: t.Prompt, Schedule: t.Schedule}
			out.Pending = append(out.Pending, jt)
		}
		for _, t := range recentFailed {
			jt := jsonTask{ID: t.ID, Prompt: t.Prompt, Error: t.Error}
			if t.FinishedAt != nil {
				jt.Elapsed = time.Since(*t.FinishedAt).Truncate(time.Second).String() + " ago"
			}
			out.Failed = append(out.Failed, jt)
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Human-readable output
	if isRunning {
		fmt.Printf("orc status: running (pid %d)\n", pid)
	} else {
		fmt.Println("orc status: stopped")
	}

	fmt.Printf("\nTasks:  %d running  %d pending  %d completed  %d failed\n",
		counts[state.TaskRunning], counts[state.TaskPending],
		counts[state.TaskCompleted], counts[state.TaskFailed])

	if len(running) > 0 {
		fmt.Println("\nRunning:")
		for _, t := range running {
			elapsed := ""
			if t.StartedAt != nil {
				elapsed = fmt.Sprintf(" (%s)", time.Since(*t.StartedAt).Truncate(time.Second))
			}
			fmt.Printf("  #%-4s %s%s\n", t.ID, config.Truncate(t.Prompt, 50), elapsed)
		}
	}

	if len(pending) > 0 {
		fmt.Println("\nPending:")
		for _, t := range pending {
			sched := ""
			if t.Schedule != "" {
				sched = " [" + t.Schedule + "]"
			}
			fmt.Printf("  #%-4s %s%s\n", t.ID, config.Truncate(t.Prompt, 50), sched)
		}
	}

	if len(recentFailed) > 0 {
		fmt.Println("\nRecent failures:")
		for _, t := range recentFailed {
			ago := ""
			if t.FinishedAt != nil {
				ago = fmt.Sprintf(" (%s ago)", time.Since(*t.FinishedAt).Truncate(time.Second))
			}
			errInfo := ""
			if t.Error != "" {
				errInfo = " — " + config.Truncate(t.Error, 40)
			}
			fmt.Printf("  #%-4s %s%s%s\n", t.ID, config.Truncate(t.Prompt, 40), errInfo, ago)
		}
	}

	return nil
}

func addCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <prompt>",
		Short: "Add a task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			schedule, _ := cmd.Flags().GetString("schedule")

			prompt := strings.Join(args, " ")

			if err := ensureInitialized(); err != nil {
				return err
			}

			store, err := state.Load()
			if err != nil {
				return err
			}

			task := state.Task{
				Prompt:    prompt,
				Schedule:  schedule,
				Status:    state.TaskPending,
				CreatedAt: time.Now(),
			}

			task, err = store.AddTask(task)
			if err != nil {
				return err
			}

			fmt.Printf("Task added: %s\n", task.ID)
			return nil
		},
	}
	cmd.Flags().StringP("schedule", "s", "", `schedule expression (e.g. "every 5m", "daily 09:00")`)
	return cmd
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all tasks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := state.Load()
			if err != nil {
				return err
			}
			printTasks(store.AllTasks())
			return nil
		},
	}
}

func printTasks(tasks []state.Task) {
	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return
	}
	fmt.Printf("%-10s %-12s %-10s %s\n", "ID", "STATUS", "SCHEDULE", "PROMPT")
	fmt.Println(strings.Repeat("-", 70))
	for _, t := range tasks {
		sched := t.Schedule
		if sched == "" {
			sched = "-"
		}
		fmt.Printf("%-10s %-12s %-10s %s\n", t.ID, t.Status, sched, config.Truncate(t.Prompt, 40))
	}
}

func removeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm [task-id]",
		Aliases: []string{"remove"},
		Short:   "Remove a task",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := ""
			if len(args) > 0 {
				taskID = args[0]
			} else {
				taskID = pickTask("Select task to remove:")
			}
			if taskID == "" {
				return nil
			}

			store, err := state.Load()
			if err != nil {
				return err
			}
			if err := store.RemoveTask(taskID); err != nil {
				return err
			}
			fmt.Printf("Task removed: %s\n", taskID)
			return nil
		},
	}
}

func reportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report [today|yesterday|YYYY-MM-DD]",
		Short: "View completed task reports",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				return err
			}

			if len(r.Entries) == 0 {
				fmt.Printf("No completed tasks for %s\n", date)
				return nil
			}

			fmt.Printf("Tasks completed on %s:\n", date)
			fmt.Println(strings.Repeat("-", 60))
			for _, entry := range r.Entries {
				fmt.Printf("\n[%s] %s\n", entry.TaskID, entry.Prompt)
				fmt.Printf("  Status: %s\n", entry.Status)
				fmt.Printf("  Finished: %s\n", entry.FinishedAt.Format("15:04:05"))
			}
			return nil
		},
	}
}

func logCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "log",
		Aliases: []string{"logs"},
		Short:   "View logs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			date, _ := cmd.Flags().GetString("date")
			follow, _ := cmd.Flags().GetBool("follow")
			taskID, _ := cmd.Flags().GetString("task")
			level, _ := cmd.Flags().GetString("level")
			outputOnly, _ := cmd.Flags().GetBool("output-only")

			taskFlagSet := cmd.Flags().Changed("task")

			if taskID != "" || taskFlagSet {
				id := taskID
				if id == "" {
					id = pickTask("Select task to view logs:")
				}
				if id == "" {
					return nil
				}

				// --output-only: show only agent stdout
				if outputOnly {
					logPath := filepath.Join(config.OrcDir(), "workdirs", id, "output.log")
					if follow {
						streamFile(logPath)
						return nil
					}
					data, err := os.ReadFile(logPath)
					if err != nil {
						return fmt.Errorf("no output log for task %s\n\n  The task may not have started yet, or the log file was removed.", id)
					}
					fmt.Print(string(data))
					return nil
				}

				// Default: interleaved orchestrator task logs + agent output
				if follow {
					logPath := filepath.Join(config.OrcDir(), "workdirs", id, "output.log")
					streamFile(logPath)
					return nil
				}

				lines, err := logging.ReadTaskLog(id)
				if err != nil {
					return err
				}
				for _, line := range lines {
					fmt.Println(line)
				}
				return nil
			}

			if follow {
				ch, cancel, err := logging.StreamLog(date, true, level)
				if err != nil {
					return err
				}
				defer cancel()
				for line := range ch {
					fmt.Println(line)
				}
				return nil
			}

			lines, err := logging.ReadLog(date, level)
			if err != nil {
				return err
			}
			for _, line := range lines {
				fmt.Println(line)
			}
			return nil
		},
	}
	cmd.Flags().StringP("date", "d", time.Now().Format("2006-01-02"), "date to view logs for (YYYY-MM-DD)")
	cmd.Flags().BoolP("follow", "f", false, "follow/stream log output")
	cmd.Flags().StringP("task", "t", "", "task ID to view logs for")
	cmd.Flags().StringP("level", "l", "all", `log level filter: "all", "orc", or "task"`)
	cmd.Flags().Bool("output-only", false, "show only agent output (use with -t)")
	return cmd
}

func streamFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not open log file: %s\n", path)
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
