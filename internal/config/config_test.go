package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaults(t *testing.T) {
	// Clear env vars that might leak from environment.
	clearEnv(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Port)
	assert.NotEmpty(t, cfg.DataDir)
	assert.Equal(t, 600*time.Second, cfg.QuestionTimeout)
	assert.Equal(t, 1800*time.Second, cfg.WaitingReminder)
	assert.Equal(t, "claude", cfg.Claude.Binary)
	assert.Equal(t, "/var/run/docker.sock", cfg.Docker.Socket)
}

func TestEnvVarOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("KODAMA_PORT", "9090")
	t.Setenv("KODAMA_DATA_DIR", "/tmp/data")
	t.Setenv("KODAMA_QUESTION_TIMEOUT", "60")
	t.Setenv("KODAMA_WAITING_REMINDER", "15")
	t.Setenv("KODAMA_CLAUDE_BINARY", "/usr/local/bin/claude")
	t.Setenv("KODAMA_DOCKER_SOCKET", "/custom/docker.sock")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "/tmp/data", cfg.DataDir)
	assert.Equal(t, 60*time.Second, cfg.QuestionTimeout)
	assert.Equal(t, 15*time.Second, cfg.WaitingReminder)
	assert.Equal(t, "/usr/local/bin/claude", cfg.Claude.Binary)
	assert.Equal(t, "/custom/docker.sock", cfg.Docker.Socket)
}

func clearEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"KODAMA_PORT", "KODAMA_DATA_DIR", "KODAMA_QUESTION_TIMEOUT",
		"KODAMA_WAITING_REMINDER",
		"KODAMA_CLAUDE_BINARY", "KODAMA_DOCKER_SOCKET",
	}
	for _, v := range vars {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}
