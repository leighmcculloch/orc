package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leighmcculloch/orc/agent"
	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/logging"
	"github.com/leighmcculloch/orc/notify"
	"github.com/leighmcculloch/orc/report"
	"github.com/leighmcculloch/orc/state"
)

type Orchestrator struct {
	cfg    config.Config
	store  *state.Store
	logger *logging.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu            sync.Mutex
	runningCount  int
	maxConcurrent int
	stopCh        chan struct{}
	eventCh       chan Event
	wg            sync.WaitGroup

	schedules map[string]*scheduleEntry
}

type scheduleEntry struct {
	taskID  string
	cron    string
	nextRun time.Time
}

type EventType int

const (
	EventTaskAdded EventType = iota
	EventTaskStarted
	EventTaskCompleted
	EventTaskFailed
	EventTaskRemoved
	EventTaskRetrying
	EventLog
)

type Event struct {
	Type    EventType
	TaskID  string
	Message string
	Time    time.Time
}

func New(cfg config.Config, store *state.Store, logger *logging.Logger) (*Orchestrator, error) {
	ctx, cancel := context.WithCancel(context.Background())

	o := &Orchestrator{
		cfg:           cfg,
		store:         store,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		maxConcurrent: cfg.Defaults.MaxConcurrent,
		stopCh:        make(chan struct{}),
		eventCh:       make(chan Event, 100),
		schedules:     make(map[string]*scheduleEntry),
	}

	return o, nil
}

func (o *Orchestrator) Events() <-chan Event {
	return o.eventCh
}

func (o *Orchestrator) emit(evt Event) {
	evt.Time = time.Now()
	select {
	case o.eventCh <- evt:
	default:
	}
}

func (o *Orchestrator) Run() error {
	o.logger.Log("orc started (max_concurrent: %d)", o.maxConcurrent)

	if err := config.WritePid(); err != nil {
		return err
	}
	defer config.RemovePid()

	// Run log cleanup once at startup
	o.cleanupOldFiles()

	// Set up schedules for existing tasks
	for _, task := range o.store.AllTasks() {
		if task.Schedule != "" && task.Status == state.TaskPending {
			o.addSchedule(task)
		}
	}

	// Main loop
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			o.logger.Log("orc shutting down, waiting for running tasks...")
			o.wg.Wait()
			o.logger.Log("orc shutdown complete")
			return nil
		case <-o.stopCh:
			o.logger.Log("orc received stop command")
			o.cancel()
			o.logger.Log("waiting for running tasks...")
			o.wg.Wait()
			o.logger.Log("orc shutdown complete")
			return nil
		case <-ticker.C:
			o.tick()
		}
	}
}

func (o *Orchestrator) tick() {
	// Check for new tasks dropped into jobs/inbox/ by orc-add
	o.pollJobInbox()

	// Check for scheduled tasks
	now := time.Now()
	for _, sched := range o.schedules {
		if now.After(sched.nextRun) {
			// Re-queue the task
			if task, ok := o.store.GetTask(sched.taskID); ok {
				if task.Status == state.TaskCompleted || task.Status == state.TaskFailed {
					// Reset task for next run
					o.store.UpdateTask(task.ID, func(t *state.Task) {
						t.Status = state.TaskPending
						t.StartedAt = nil
						t.FinishedAt = nil
						t.Error = ""
					})
					o.logger.SchedLog("re-queued task %s (%s)", task.ID, sched.cron)
				}
			}
			sched.nextRun = nextScheduleTime(sched.cron)
		}
	}

	// Reload store to pick up tasks added by other processes
	if fresh, err := state.Load(); err == nil {
		o.store.Merge(fresh)
	}

	// Start pending tasks if capacity available
	o.mu.Lock()
	available := o.maxConcurrent - o.runningCount
	o.mu.Unlock()

	if available <= 0 {
		return
	}

	pending := o.store.TasksByStatus(state.TaskPending)
	started := 0
	for i := 0; i < len(pending) && started < available; i++ {
		task := pending[i]
		// Skip tasks with a retry backoff that hasn't elapsed
		if task.RetryAfter != nil && time.Now().Before(*task.RetryAfter) {
			continue
		}
		// Only start non-scheduled tasks immediately, or scheduled tasks whose time has come
		if task.Schedule != "" {
			if sched, ok := o.schedules[task.ID]; ok {
				if time.Now().Before(sched.nextRun) {
					continue
				}
			}
		}
		o.startTask(task)
		started++
	}
}

func (o *Orchestrator) startTask(task state.Task) {
	o.mu.Lock()
	o.runningCount++
	o.mu.Unlock()
	o.wg.Add(1)

	now := time.Now()
	o.store.UpdateTask(task.ID, func(t *state.Task) {
		t.Status = state.TaskRunning
		t.StartedAt = &now
	})

	o.logger.TaskLog(task.ID, "starting task: %s", task.Prompt)
	o.emit(Event{Type: EventTaskStarted, TaskID: task.ID, Message: task.Prompt})

	go func() {
		defer o.wg.Done()
		defer func() {
			o.mu.Lock()
			o.runningCount--
			o.mu.Unlock()
		}()

		result := agent.Run(o.ctx, o.cfg, task.ID, task.Prompt, func(format string, args ...any) {
			o.logger.TaskLog(task.ID, format, args...)
		})

		now := time.Now()
		if result.Error != nil && o.ctx.Err() != nil {
			// Context cancelled, task was interrupted
			o.store.UpdateTask(task.ID, func(t *state.Task) {
				t.Status = state.TaskCancelled
				t.FinishedAt = &now
				t.Error = "cancelled"
			})
			o.logger.TaskLog(task.ID, "task cancelled")
			o.emit(Event{Type: EventTaskFailed, TaskID: task.ID, Message: "cancelled"})
			return
		}

		if result.ExitCode != 0 {
			errMsg := ""
			if result.Error != nil {
				errMsg = result.Error.Error()
			}

			// Check if we should retry
			maxRetries := o.cfg.Defaults.MaxRetries
			// Refresh task to get current RetryCount and per-task override
			current, _ := o.store.GetTask(task.ID)
			if current.MaxRetries > 0 {
				maxRetries = current.MaxRetries
			}

			// Do not retry timeout failures (context.DeadlineExceeded)
			isTimeout := result.Error == context.DeadlineExceeded

			if maxRetries > 0 && current.RetryCount < maxRetries && !isTimeout {
				// Parse backoff duration
				backoff := 30 * time.Second
				if o.cfg.Defaults.RetryBackoff != "" {
					if d, err := time.ParseDuration(o.cfg.Defaults.RetryBackoff); err == nil {
						backoff = d
					}
				}
				retryAfter := now.Add(backoff)
				attempt := current.RetryCount + 1

				o.store.UpdateTask(task.ID, func(t *state.Task) {
					t.Status = state.TaskPending
					t.StartedAt = nil
					t.FinishedAt = nil
					t.Error = ""
					t.RetryCount = attempt
					t.RetryAfter = &retryAfter
				})
				o.logger.TaskLog(task.ID, "task failed, retrying (attempt %d/%d) after %s", attempt, maxRetries, backoff)
				o.emit(Event{Type: EventTaskRetrying, TaskID: task.ID, Message: fmt.Sprintf("retry %d/%d after %s", attempt, maxRetries, backoff)})
			} else {
				o.store.UpdateTask(task.ID, func(t *state.Task) {
					t.Status = state.TaskFailed
					t.FinishedAt = &now
					t.Error = errMsg
				})
				o.logger.TaskLog(task.ID, "task failed: %s", errMsg)
				o.emit(Event{Type: EventTaskFailed, TaskID: task.ID, Message: errMsg})
				o.notify(fmt.Sprintf("Task %s failed", task.ID), config.Truncate(errMsg, 100))
			}
		} else {
			o.store.UpdateTask(task.ID, func(t *state.Task) {
				t.Status = state.TaskCompleted
				t.FinishedAt = &now
			})
			o.logger.TaskLog(task.ID, "task completed")
			o.emit(Event{Type: EventTaskCompleted, TaskID: task.ID})
			o.notify(fmt.Sprintf("Task %s completed", task.ID), config.Truncate(task.Prompt, 100))
		}

		// Record in daily report
		if updated, ok := o.store.GetTask(task.ID); ok {
			report.RecordCompletion(updated)
		}
	}()
}

// pollJobInbox checks for prompt files dropped by orc-add into jobs/inbox/.
func (o *Orchestrator) pollJobInbox() {
	inboxDir := filepath.Join(config.OrcDir(), "jobs", "inbox")
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		path := filepath.Join(inboxDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		os.Remove(path)

		prompt := string(data)
		if prompt == "" {
			continue
		}

		task := state.Task{
			Prompt:    prompt,
			Status:    state.TaskPending,
			CreatedAt: time.Now(),
		}
		task, err = o.store.AddTask(task)
		if err != nil {
			o.logger.Log("error adding task from inbox: %v", err)
			continue
		}
		o.logger.Log("task added from inbox: %s (%s)", task.ID, config.Truncate(task.Prompt, 60))
		o.emit(Event{Type: EventTaskAdded, TaskID: task.ID, Message: task.Prompt})
	}
}

func (o *Orchestrator) addSchedule(task state.Task) {
	o.schedules[task.ID] = &scheduleEntry{
		taskID:  task.ID,
		cron:    task.Schedule,
		nextRun: nextScheduleTime(task.Schedule),
	}
}

func (o *Orchestrator) Stop() {
	select {
	case <-o.stopCh:
	default:
		close(o.stopCh)
	}
}

func (o *Orchestrator) Store() *state.Store {
	return o.store
}

func (o *Orchestrator) notify(title, body string) {
	if o.cfg.Notifications.Desktop {
		notify.Send(title, body)
	}
}

// cleanupOldFiles removes log files, reports, and workdirs older than the retention period.
func (o *Orchestrator) cleanupOldFiles() {
	days := o.cfg.Defaults.LogRetentionDays
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	var logCount, workdirCount int

	// Clean up log files
	logsDir := filepath.Join(config.OrcDir(), "logs")
	if entries, err := os.ReadDir(logsDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(logsDir, e.Name()))
				logCount++
			}
		}
	}

	// Clean up report files
	reportsDir := filepath.Join(config.OrcDir(), "reports")
	if entries, err := os.ReadDir(reportsDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(reportsDir, e.Name()))
				logCount++
			}
		}
	}

	// Clean up workdirs for completed/failed tasks older than retention
	workdirsDir := filepath.Join(config.OrcDir(), "workdirs")
	if entries, err := os.ReadDir(workdirsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			taskID := e.Name()
			task, ok := o.store.GetTask(taskID)
			if !ok {
				continue
			}
			if (task.Status == state.TaskCompleted || task.Status == state.TaskFailed) &&
				task.FinishedAt != nil && task.FinishedAt.Before(cutoff) {
				os.RemoveAll(filepath.Join(workdirsDir, taskID))
				workdirCount++
			}
		}
	}

	if logCount > 0 || workdirCount > 0 {
		o.logger.Log("cleaned up %d log/report files and %d workdirs older than %d days", logCount, workdirCount, days)
	}
}
