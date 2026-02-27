package config

import (
	"os"
	"path/filepath"
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
	assert.Equal(t, "./data", cfg.DataDir)
	assert.Equal(t, 600*time.Second, cfg.QuestionTimeout)
	assert.Equal(t, "claude", cfg.Claude.Binary)
	assert.Equal(t, "/var/run/docker.sock", cfg.Docker.Socket)
}

func TestEnvVarOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("KODAMA_PORT", "9090")
	t.Setenv("KODAMA_DATA_DIR", "/tmp/data")
	t.Setenv("KODAMA_QUESTION_TIMEOUT", "60")
	t.Setenv("KODAMA_TELEGRAM_TOKEN", "mytoken")
	t.Setenv("KODAMA_TELEGRAM_USER_ID", "12345")
	t.Setenv("KODAMA_CLAUDE_BINARY", "/usr/local/bin/claude")
	t.Setenv("KODAMA_DOCKER_SOCKET", "/custom/docker.sock")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "/tmp/data", cfg.DataDir)
	assert.Equal(t, 60*time.Second, cfg.QuestionTimeout)
	assert.Equal(t, "mytoken", cfg.Telegram.Token)
	assert.Equal(t, int64(12345), cfg.Telegram.UserID)
	assert.Equal(t, "/usr/local/bin/claude", cfg.Claude.Binary)
	assert.Equal(t, "/custom/docker.sock", cfg.Docker.Socket)
}

func TestYAMLConfigFile(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kodama-server.yml")
	content := `
port: 7070
data_dir: /var/kodama
question_timeout: 45
telegram:
  token: telegram-token
  user_id: 99999
docker:
  socket: /run/docker.sock
claude:
  binary: /bin/claude
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

	// Change to the temp dir so ./kodama-server.yml is found.
	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 7070, cfg.Port)
	assert.Equal(t, "/var/kodama", cfg.DataDir)
	assert.Equal(t, 45*time.Second, cfg.QuestionTimeout)
	assert.Equal(t, "telegram-token", cfg.Telegram.Token)
	assert.Equal(t, int64(99999), cfg.Telegram.UserID)
	assert.Equal(t, "/run/docker.sock", cfg.Docker.Socket)
	assert.Equal(t, "/bin/claude", cfg.Claude.Binary)
}

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kodama-server.yml")
	content := `port: 7070`
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	t.Setenv("KODAMA_PORT", "3333")

	cfg, err := Load()
	require.NoError(t, err)

	// Env var wins over file.
	assert.Equal(t, 3333, cfg.Port)
}

func clearEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"KODAMA_PORT", "KODAMA_DATA_DIR", "KODAMA_QUESTION_TIMEOUT",
		"KODAMA_TELEGRAM_TOKEN", "KODAMA_TELEGRAM_USER_ID",
		"KODAMA_CLAUDE_BINARY", "KODAMA_DOCKER_SOCKET",
	}
	for _, v := range vars {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}
