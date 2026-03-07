package logging

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leighmcculloch/orc/config"
)

type Logger struct {
	mu        sync.Mutex
	file      *os.File
	writers   []io.Writer
	listeners map[int]chan string
	nextID    int
}

func New() (*Logger, error) {
	if err := config.EnsureOrcDir(); err != nil {
		return nil, err
	}
	logPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("orc-%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	return &Logger{
		file:      f,
		writers:   []io.Writer{f},
		listeners: make(map[int]chan string),
	}, nil
}

func (l *Logger) Log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [orc] %s\n", time.Now().Format("15:04:05"), msg)
	for _, w := range l.writers {
		w.Write([]byte(line))
	}
	for _, ch := range l.listeners {
		select {
		case ch <- line:
		default:
		}
	}
}

// SchedLog logs schedule-related events with the [sched] prefix.
func (l *Logger) SchedLog(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [sched] %s\n", time.Now().Format("15:04:05"), msg)
	for _, w := range l.writers {
		w.Write([]byte(line))
	}
	for _, ch := range l.listeners {
		select {
		case ch <- line:
		default:
		}
	}
}

func (l *Logger) TaskLog(taskID string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.logRaw(fmt.Sprintf("[%s] [task:%s] %s\n", time.Now().Format("15:04:05"), taskID, msg))
	l.writeTaskLog(taskID, msg)
}

// logRaw writes a pre-formatted line to all writers and listeners.
func (l *Logger) logRaw(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.writers {
		w.Write([]byte(line))
	}
	for _, ch := range l.listeners {
		select {
		case ch <- line:
		default:
		}
	}
}

func (l *Logger) writeTaskLog(taskID string, msg string) {
	logDir := filepath.Join(config.OrcDir(), "logs")
	logPath := filepath.Join(logDir, fmt.Sprintf("task-%s.log", taskID))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
}

func (l *Logger) Subscribe() (int, chan string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := l.nextID
	l.nextID++
	ch := make(chan string, 100)
	l.listeners[id] = ch
	return id, ch
}

func (l *Logger) Unsubscribe(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, ok := l.listeners[id]; ok {
		close(ch)
		delete(l.listeners, id)
	}
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, ch := range l.listeners {
		close(ch)
		delete(l.listeners, id)
	}
	return l.file.Close()
}

// ReadLog reads the orchestrator log for a date, optionally filtered by level.
// Level can be "all" (default), "orc" (orchestrator only), or "task" (task events only).
func ReadLog(date string, level string) ([]string, error) {
	logPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("orc-%s.log", date))
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no logs found for %s\n\n  Orc may not have been running on that date, or logs were cleared.", date)
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if matchesLevel(line, level) {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// ReadTaskLog reads the interleaved orchestrator task logs and agent output for a task.
// Orchestrator lines get [orc] prefix, agent output lines are shown as-is.
func ReadTaskLog(taskID string) ([]string, error) {
	var lines []string

	// Read orchestrator task log
	taskLogPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("task-%s.log", taskID))
	if data, err := os.ReadFile(taskLogPath); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				lines = append(lines, "[orc] "+line)
			}
		}
	}

	// Read agent output
	outputPath := filepath.Join(config.OrcDir(), "workdirs", taskID, "output.log")
	if data, err := os.ReadFile(outputPath); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return nil, fmt.Errorf("no logs found for task %s\n\n  The task may not have started yet, or the log files were removed.", taskID)
	}

	return lines, nil
}

func matchesLevel(line string, level string) bool {
	switch level {
	case "orc":
		return strings.Contains(line, "[orc]") || strings.Contains(line, "[sched]")
	case "task":
		return strings.Contains(line, "[task:")
	default:
		return true
	}
}

func StreamLog(date string, follow bool, level string) (<-chan string, func(), error) {
	logPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("orc-%s.log", date))
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("no logs found for %s\n\n  Orc may not have been running on that date, or logs were cleared.", date)
		}
		return nil, nil, err
	}

	ch := make(chan string, 100)
	done := make(chan struct{})

	go func() {
		defer f.Close()
		defer close(ch)
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if !matchesLevel(line, level) {
				continue
			}
			select {
			case ch <- line:
			case <-done:
				return
			}
		}
		if !follow {
			return
		}
		for {
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
				if scanner.Scan() {
					line := scanner.Text()
					if matchesLevel(line, level) {
						ch <- line
					}
				}
			}
		}
	}()

	cancel := func() { close(done) }
	return ch, cancel, nil
}
