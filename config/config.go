package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Environments map[string]Environment `json:"environments"`
	Defaults     Defaults               `json:"defaults"`
}

type Environment struct {
	Name     string   `json:"name"`
	WorkDir  string   `json:"work_dir"`
	PreHooks []string `json:"pre_hooks"`
}

type Defaults struct {
	Environment    string `json:"environment"`
	MaxConcurrent  int    `json:"max_concurrent"`
	ClaudeCodePath string `json:"claude_code_path"`
}

func DefaultConfig() Config {
	return Config{
		Environments: map[string]Environment{
			"default": {
				Name:     "default",
				WorkDir:  ".",
				PreHooks: []string{},
			},
		},
		Defaults: Defaults{
			Environment:    "default",
			MaxConcurrent:  3,
			ClaudeCodePath: "claude",
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
		filepath.Join(OrcDir(), "reports"),
		filepath.Join(OrcDir(), "inbox"),
		filepath.Join(OrcDir(), "outbox"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

func ConfigPath() string {
	return filepath.Join(OrcDir(), "config.json")
}

func Load() (Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Defaults.MaxConcurrent == 0 {
		cfg.Defaults.MaxConcurrent = 3
	}
	if cfg.Defaults.ClaudeCodePath == "" {
		cfg.Defaults.ClaudeCodePath = "claude"
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
