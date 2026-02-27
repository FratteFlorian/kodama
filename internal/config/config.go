package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all Kodama configuration.
type Config struct {
	Port            int            `yaml:"port"`
	DataDir         string         `yaml:"data_dir"`
	QuestionTimeout time.Duration  `yaml:"-"`
	QuestionTimeoutS int           `yaml:"question_timeout"` // seconds, used for YAML parsing
	Telegram        TelegramConfig `yaml:"telegram"`
	Docker          DockerConfig   `yaml:"docker"`
	Claude          ClaudeConfig   `yaml:"claude"`
}

// TelegramConfig holds Telegram bot settings.
type TelegramConfig struct {
	Token  string `yaml:"token"`
	UserID int64  `yaml:"user_id"`
}

// DockerConfig holds Docker settings.
type DockerConfig struct {
	Socket string `yaml:"socket"`
}

// ClaudeConfig holds Claude CLI settings.
type ClaudeConfig struct {
	Binary string `yaml:"binary"`
}

// defaults returns a Config with all defaults applied.
func defaults() Config {
	return Config{
		Port:             8080,
		DataDir:          "./data",
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
// File search order: ./kodama-server.yml, ~/.config/kodama/config.yml
// Environment variables override file values.
func Load() (*Config, error) {
	cfg := defaults()

	// Try config files in order.
	candidates := []string{
		"./kodama-server.yml",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "kodama", "config.yml"))
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
		break
	}

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
	if v := os.Getenv("KODAMA_TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("KODAMA_TELEGRAM_USER_ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Telegram.UserID = n
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
