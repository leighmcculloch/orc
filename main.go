package main

import (
	"bufio"
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

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "orc",
		Short: "AI Agent Orchestrator",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrchestrator()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddGroup(
		&cobra.Group{ID: "orchestrator", Title: "Orchestrator:"},
		&cobra.Group{ID: "tasks", Title: "Tasks:"},
	)

	run := runCmd()
	run.GroupID = "orchestrator"
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

	rootCmd.AddCommand(run, log, add, ls, rm, rpt)

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
	if err := store.Save(); err != nil {
		return err
	}
	return agent.WriteOrcAddScript()
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

func addCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <prompt>",
		Short: "Add a task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _ := cmd.Flags().GetString("env")
			schedule, _ := cmd.Flags().GetString("schedule")

			prompt := strings.Join(args, " ")

			if err := ensureInitialized(); err != nil {
				return err
			}

			store, err := state.Load()
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			taskEnv := env
			if taskEnv == "" {
				taskEnv = cfg.Defaults.Environment
			}

			task := state.Task{
				Prompt:      prompt,
				Environment: taskEnv,
				Schedule:    schedule,
				Status:      state.TaskPending,
				CreatedAt:   time.Now(),
			}

			task, err = store.AddTask(task)
			if err != nil {
				return err
			}

			fmt.Printf("Task added: %s\n", task.ID)
			return nil
		},
	}
	cmd.Flags().StringP("env", "e", "", "environment name")
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

			taskFlagSet := cmd.Flags().Changed("task")

			if taskID != "" || taskFlagSet {
				id := taskID
				if id == "" {
					id = pickTask("Select task to view logs:")
				}
				if id == "" {
					return nil
				}
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

			if follow {
				ch, cancel, err := logging.StreamLog(date, true)
				if err != nil {
					return err
				}
				defer cancel()
				for line := range ch {
					fmt.Println(line)
				}
				return nil
			}

			lines, err := logging.ReadLog(date)
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
