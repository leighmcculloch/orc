package logging

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), msg)
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
	l.Log("[task:%s] %s", taskID, msg)
	l.writeTaskLog(taskID, msg)
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

func TaskLogPath(taskID string) string {
	return filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("task-%s.log", taskID))
}

func ReadLog(date string) ([]string, error) {
	logPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("orc-%s.log", date))
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no log file for date %s", date)
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func StreamLog(date string, follow bool) (<-chan string, func(), error) {
	logPath := filepath.Join(config.OrcDir(), "logs", fmt.Sprintf("orc-%s.log", date))
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("no log file for date %s", date)
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
			select {
			case ch <- scanner.Text():
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
					ch <- scanner.Text()
				}
			}
		}
	}()

	cancel := func() { close(done) }
	return ch, cancel, nil
}
