package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds all Kodama configuration.
type Config struct {
	Port             int
	DataDir          string
	QuestionTimeout  time.Duration `yaml:"-"`
	QuestionTimeoutS int
	Docker           DockerConfig
	Claude           ClaudeConfig
}

// DockerConfig holds Docker settings.
type DockerConfig struct {
	Socket string
}

// ClaudeConfig holds Claude CLI settings.
type ClaudeConfig struct {
	Binary string
}

// defaults returns a Config with all defaults applied.
func defaults() Config {
	homeDir, err := os.UserHomeDir()
	dataDir := "./.kodama"
	if err == nil && homeDir != "" {
		dataDir = filepath.Join(homeDir, ".kodama")
	}
	return Config{
		Port:             8080,
		DataDir:          dataDir,
		QuestionTimeoutS: 600, // 10 minutes — claude --print can take 60-120s before producing output
		Docker: DockerConfig{
			Socket: "/var/run/docker.sock",
		},
		Claude: ClaudeConfig{
			Binary: "claude",
		},
	}
}

// Load reads configuration from files and environment variables.
// Environment variables override defaults.
func Load() (*Config, error) {
	cfg := defaults()

	// Overlay environment variables.
	if v := os.Getenv("KODAMA_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Port = n
		}
	}
	if v := os.Getenv("KODAMA_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("KODAMA_QUESTION_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.QuestionTimeoutS = n
		}
	}
	if v := os.Getenv("KODAMA_CLAUDE_BINARY"); v != "" {
		cfg.Claude.Binary = v
	}
	if v := os.Getenv("KODAMA_DOCKER_SOCKET"); v != "" {
		cfg.Docker.Socket = v
	}

	// Convert seconds to duration.
	if cfg.QuestionTimeoutS <= 0 {
		cfg.QuestionTimeoutS = 600
	}
	cfg.QuestionTimeout = time.Duration(cfg.QuestionTimeoutS) * time.Second

	return &cfg, nil
}
