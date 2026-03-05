package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/tidwall/jsonc"
)

type Config struct {
	Environments map[string]Environment `json:"environments"`
	Defaults     Defaults               `json:"defaults"`
}

type Environment struct {
	Name    string `json:"name"`
	WorkDir string `json:"work_dir"`
}

type Defaults struct {
	Environment   string `json:"environment"`
	MaxConcurrent int    `json:"max_concurrent"`
	Command       string `json:"command"`
}

func DefaultConfig() Config {
	return Config{
		Environments: map[string]Environment{
			"default": {
				Name:    "default",
				WorkDir: ".",
			},
		},
		Defaults: Defaults{
			Environment:   "default",
			MaxConcurrent: 3,
		},
	}
}

func OrcDir() string {
	return ".orc"
}

func EnsureOrcDir() error {
	dirs := []string{
		OrcDir(),
		filepath.Join(OrcDir(), "logs"),
		filepath.Join(OrcDir(), "workdirs"),
		filepath.Join(OrcDir(), "jobs"),
		filepath.Join(OrcDir(), "jobs", "inbox"),
		filepath.Join(OrcDir(), "reports"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

func ConfigPath() string {
	return filepath.Join(OrcDir(), "config.jsonc")
}

func Load() (Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(jsonc.ToJSON(data), &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config in %s: %w", path, err)
	}
	if cfg.Defaults.MaxConcurrent == 0 {
		cfg.Defaults.MaxConcurrent = 3
	}
	if cfg.Environments == nil {
		cfg.Environments = make(map[string]Environment)
	}
	return cfg, nil
}

func Save(cfg Config) error {
	if err := EnsureOrcDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(ConfigPath(), data, 0644)
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// PidPath returns the path to the pid file.
func PidPath() string {
	return filepath.Join(OrcDir(), "orc.pid")
}

// IsRunning checks if orc is running by reading the pid file and verifying
// the process is alive. Removes stale pid files.
func IsRunning() bool {
	_, ok := RunningPid()
	return ok
}

// RunningPid returns the pid of the running orc process, or false if not running.
func RunningPid() (int, bool) {
	data, err := os.ReadFile(PidPath())
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		os.Remove(PidPath())
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(PidPath())
		return 0, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(PidPath())
		return 0, false
	}
	return pid, true
}

// WritePid writes the current process pid to the pid file.
func WritePid() error {
	return os.WriteFile(PidPath(), []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
}

// RemovePid removes the pid file.
func RemovePid() {
	os.Remove(PidPath())
}
