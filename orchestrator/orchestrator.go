package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/leighmcculloch/orc/agent"
	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/ipc"
	"github.com/leighmcculloch/orc/logging"
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

	if err := ipc.EnsureIPCDirs(); err != nil {
		cancel()
		return nil, err
	}

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

	// Write pid file so other processes know we're running
	if err := ipc.WritePid(); err != nil {
		return fmt.Errorf("writing pid file: %w", err)
	}
	defer ipc.RemovePid()

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
			o.pollInbox()
			o.tick()
		}
	}
}

// pollInbox checks the inbox directory for new command files.
func (o *Orchestrator) pollInbox() {
	requests, err := ipc.PollInbox()
	if err != nil {
		o.logger.Log("error polling inbox: %v", err)
		return
	}
	for _, req := range requests {
		resp := o.handleIPC(req)
		if err := ipc.WriteResponse(req.ID, resp); err != nil {
			o.logger.Log("error writing response for %s: %v", req.ID, err)
		}
	}
}

func (o *Orchestrator) tick() {
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
				}
			}
			sched.nextRun = nextScheduleTime(sched.cron)
		}
	}

	// Start pending tasks if capacity available
	o.mu.Lock()
	available := o.maxConcurrent - o.runningCount
	o.mu.Unlock()

	if available <= 0 {
		return
	}

	pending := o.store.TasksByStatus(state.TaskPending)
	for i := 0; i < len(pending) && i < available; i++ {
		task := pending[i]
		// Only start non-scheduled tasks immediately, or scheduled tasks whose time has come
		if task.Schedule != "" {
			if sched, ok := o.schedules[task.ID]; ok {
				if time.Now().Before(sched.nextRun) {
					continue
				}
			}
		}
		o.startTask(task)
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

		env := task.Environment
		if env == "" {
			env = o.cfg.Defaults.Environment
		}

		result := agent.Run(o.ctx, o.cfg, task.ID, task.Prompt, env, func(format string, args ...any) {
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
			o.store.UpdateTask(task.ID, func(t *state.Task) {
				t.Status = state.TaskFailed
				t.FinishedAt = &now
				t.Error = errMsg
			})
			o.logger.TaskLog(task.ID, "task failed: %s", errMsg)
			o.emit(Event{Type: EventTaskFailed, TaskID: task.ID, Message: errMsg})
		} else {
			o.store.UpdateTask(task.ID, func(t *state.Task) {
				t.Status = state.TaskCompleted
				t.FinishedAt = &now
			})
			o.logger.TaskLog(task.ID, "task completed")
			o.emit(Event{Type: EventTaskCompleted, TaskID: task.ID})
		}

		// Record in daily report
		if updated, ok := o.store.GetTask(task.ID); ok {
			report.RecordCompletion(updated)
		}
	}()
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

func (o *Orchestrator) handleIPC(req ipc.Request) ipc.Response {
	switch req.Command {
	case ipc.CmdAddTask:
		return o.handleAddTask(req.Payload)
	case ipc.CmdListTasks:
		return o.handleListTasks()
	case ipc.CmdRemoveTask:
		return o.handleRemoveTask(req.Payload)
	case ipc.CmdGetStatus:
		return o.handleGetStatus()
	case ipc.CmdStop:
		o.Stop()
		return ipc.Response{OK: true}
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command: %s", req.Command)}
	}
}

func (o *Orchestrator) handleAddTask(payload json.RawMessage) ipc.Response {
	var p ipc.AddTaskPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return ipc.Response{OK: false, Error: "invalid payload"}
	}

	env := p.Environment
	if env == "" {
		env = o.cfg.Defaults.Environment
	}

	task := state.Task{
		ID:          generateID(),
		Prompt:      p.Prompt,
		Environment: env,
		Schedule:    p.Schedule,
		Status:      state.TaskPending,
		CreatedAt:   time.Now(),
	}

	if err := o.store.AddTask(task); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}

	if task.Schedule != "" {
		o.addSchedule(task)
	}

	o.logger.Log("task added: %s (%s)", task.ID, task.Prompt)
	o.emit(Event{Type: EventTaskAdded, TaskID: task.ID, Message: task.Prompt})

	data, _ := json.Marshal(task)
	return ipc.Response{OK: true, Payload: data}
}

func (o *Orchestrator) handleListTasks() ipc.Response {
	tasks := o.store.AllTasks()
	data, _ := json.Marshal(tasks)
	return ipc.Response{OK: true, Payload: data}
}

func (o *Orchestrator) handleRemoveTask(payload json.RawMessage) ipc.Response {
	var p ipc.RemoveTaskPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return ipc.Response{OK: false, Error: "invalid payload"}
	}

	if err := o.store.RemoveTask(p.TaskID); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}

	delete(o.schedules, p.TaskID)

	o.logger.Log("task removed: %s", p.TaskID)
	o.emit(Event{Type: EventTaskRemoved, TaskID: p.TaskID})
	return ipc.Response{OK: true}
}

func (o *Orchestrator) handleGetStatus() ipc.Response {
	status := struct {
		Running   int          `json:"running"`
		Pending   int          `json:"pending"`
		Completed int          `json:"completed"`
		Failed    int          `json:"failed"`
		Tasks     []state.Task `json:"tasks"`
	}{
		Running:   len(o.store.TasksByStatus(state.TaskRunning)),
		Pending:   len(o.store.TasksByStatus(state.TaskPending)),
		Completed: len(o.store.TasksByStatus(state.TaskCompleted)),
		Failed:    len(o.store.TasksByStatus(state.TaskFailed)),
		Tasks:     o.store.AllTasks(),
	}
	data, _ := json.Marshal(status)
	return ipc.Response{OK: true, Payload: data}
}
