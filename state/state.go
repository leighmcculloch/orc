package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/leighmcculloch/orc/config"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Task struct {
	ID          string     `json:"id"`
	Prompt      string     `json:"prompt"`
	Environment string     `json:"environment"`
	Schedule    string     `json:"schedule,omitempty"`
	Status      TaskStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	WorkDir     string     `json:"work_dir,omitempty"`
	PID         int        `json:"pid,omitempty"`
}

type meta struct {
	NextID int `json:"next_id"`
}

type Store struct {
	mu        sync.Mutex
	nextID    int
	todo      []Task // pending + running
	scheduled []Task
	completed []Task
	failed    []Task
}

func jobsDir() string {
	return filepath.Join(config.OrcDir(), "jobs")
}

func Load() (*Store, error) {
	s := &Store{nextID: 1}

	// Load meta
	if data, err := os.ReadFile(filepath.Join(jobsDir(), "meta.json")); err == nil {
		var m meta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parsing meta.json: %w", err)
		}
		s.nextID = m.NextID
	}
	if s.nextID == 0 {
		s.nextID = 1
	}

	// Load each file
	s.todo = loadFile(filepath.Join(jobsDir(), "todo.json"))
	s.scheduled = loadFile(filepath.Join(jobsDir(), "scheduled.json"))
	s.completed = loadFile(filepath.Join(jobsDir(), "completed.json"))
	s.failed = loadFile(filepath.Join(jobsDir(), "failed.json"))

	// Migrate from old state.json if jobs dir is empty
	if len(s.todo) == 0 && len(s.scheduled) == 0 && len(s.completed) == 0 && len(s.failed) == 0 {
		oldPath := filepath.Join(config.OrcDir(), "state.json")
		if data, err := os.ReadFile(oldPath); err == nil {
			var old struct {
				NextID int    `json:"next_id"`
				Tasks  []Task `json:"tasks"`
			}
			if err := json.Unmarshal(data, &old); err == nil && len(old.Tasks) > 0 {
				if old.NextID > s.nextID {
					s.nextID = old.NextID
				}
				for _, t := range old.Tasks {
					s.placeTask(t)
				}
				// Save migrated data and remove old file
				if err := s.saveLocked(); err == nil {
					os.Remove(oldPath)
				}
			}
		}
	}

	return s, nil
}

func loadFile(path string) []Task {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil
	}
	return tasks
}

// placeTask puts a task into the correct slice based on its status and schedule.
func (s *Store) placeTask(t Task) {
	switch {
	case t.Schedule != "" && (t.Status == TaskPending || t.Status == TaskRunning):
		s.scheduled = append(s.scheduled, t)
	case t.Status == TaskCompleted:
		s.completed = append(s.completed, t)
	case t.Status == TaskFailed || t.Status == TaskCancelled:
		s.failed = append(s.failed, t)
	default: // pending, running
		s.todo = append(s.todo, t)
	}
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := config.EnsureOrcDir(); err != nil {
		return err
	}
	dir := jobsDir()

	// Save meta
	m := meta{NextID: s.nextID}
	if err := writeJSON(filepath.Join(dir, "meta.json"), m); err != nil {
		return err
	}

	if err := writeJSON(filepath.Join(dir, "todo.json"), s.todo); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "scheduled.json"), s.scheduled); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "completed.json"), s.completed); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "failed.json"), s.failed)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	return os.Rename(tmp, path)
}

func (s *Store) AddTask(task Task) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextID == 0 {
		s.nextID = 1
	}
	task.ID = strconv.Itoa(s.nextID)
	s.nextID++
	s.placeTask(task)
	return task, s.saveLocked()
}

func (s *Store) UpdateTask(id string, fn func(*Task)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the task across all slices
	t, slice, idx := s.findTask(id)
	if t == nil {
		return fmt.Errorf("task %s not found", id)
	}

	oldStatus := t.Status
	oldSchedule := t.Schedule
	fn(t)

	// If status or schedule changed, move to correct slice
	if t.Status != oldStatus || t.Schedule != oldSchedule {
		// Remove from current slice
		*slice = append((*slice)[:idx], (*slice)[idx+1:]...)
		// Place in correct slice
		s.placeTask(*t)
	} else {
		// Update in place
		(*slice)[idx] = *t
	}

	return s.saveLocked()
}

func (s *Store) RemoveTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, slice, idx := s.findTask(id)
	if slice == nil {
		return fmt.Errorf("task %s not found", id)
	}
	*slice = append((*slice)[:idx], (*slice)[idx+1:]...)
	return s.saveLocked()
}

func (s *Store) GetTask(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, _ := s.findTask(id)
	if t == nil {
		return Task{}, false
	}
	return *t, true
}

func (s *Store) TasksByStatus(status TaskStatus) []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Task
	for _, t := range s.allTasksLocked() {
		if t.Status == status {
			result = append(result, t)
		}
	}
	return result
}

// Merge incorporates new tasks from a freshly loaded store that don't exist
// in the current store. This allows the orchestrator to pick up tasks added
// by other processes writing directly to the job files.
func (s *Store) Merge(other *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := make(map[string]bool)
	for _, t := range s.allTasksLocked() {
		existing[t.ID] = true
	}

	other.mu.Lock()
	defer other.mu.Unlock()
	for _, t := range other.allTasksLocked() {
		if !existing[t.ID] {
			s.placeTask(t)
		}
	}

	// Update nextID if the other store has a higher value
	if other.nextID > s.nextID {
		s.nextID = other.nextID
	}
}

func (s *Store) AllTasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allTasksLocked()
}

func (s *Store) allTasksLocked() []Task {
	result := make([]Task, 0, len(s.todo)+len(s.scheduled)+len(s.completed)+len(s.failed))
	result = append(result, s.todo...)
	result = append(result, s.scheduled...)
	result = append(result, s.completed...)
	result = append(result, s.failed...)
	return result
}

// findTask searches all slices and returns a pointer to the task, the slice it's in, and its index.
func (s *Store) findTask(id string) (*Task, *[]Task, int) {
	for _, slice := range []*[]Task{&s.todo, &s.scheduled, &s.completed, &s.failed} {
		for i := range *slice {
			if (*slice)[i].ID == id {
				return &(*slice)[i], slice, i
			}
		}
	}
	return nil, nil, -1
}
