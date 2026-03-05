package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

type Store struct {
	Tasks     []Task    `json:"tasks"`
	UpdatedAt time.Time `json:"updated_at"`
	mu        sync.Mutex
}

func statePath() string {
	return filepath.Join(config.OrcDir(), "state.json")
}

func Load() (*Store, error) {
	path := statePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{Tasks: []Task{}}, nil
		}
		return nil, fmt.Errorf("reading state: %w", err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	if s.Tasks == nil {
		s.Tasks = []Task{}
	}
	return &s, nil
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
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	return os.Rename(tmp, statePath())
}

func (s *Store) AddTask(task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Tasks = append(s.Tasks, task)
	return s.saveLocked()
}

func (s *Store) UpdateTask(id string, fn func(*Task)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			fn(&s.Tasks[i])
			return s.saveLocked()
		}
	}
	return fmt.Errorf("task %s not found", id)
}

func (s *Store) RemoveTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			return s.saveLocked()
		}
	}
	return fmt.Errorf("task %s not found", id)
}

func (s *Store) GetTask(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

func (s *Store) TasksByStatus(status TaskStatus) []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Task
	for _, t := range s.Tasks {
		if t.Status == status {
			result = append(result, t)
		}
	}
	return result
}

func (s *Store) AllTasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Task, len(s.Tasks))
	copy(result, s.Tasks)
	return result
}
