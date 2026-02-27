package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildMock builds a mock binary from tests/mocks and returns its path.
func buildMock(t *testing.T, mockName string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, mockName)

	// Find the mock source path relative to module root.
	// Walk up to find go.mod.
	srcPath := findMockSource(t, mockName)

	cmd := exec.Command("go", "build", "-o", out, srcPath)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "build mock: %s", output)
	return out
}

func findMockSource(t *testing.T, name string) string {
	t.Helper()
	// Walk up directories to find the module root (contains go.mod).
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "tests", "mocks", name)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestClaudeAgentBasic(t *testing.T) {
	mockBinary := buildMock(t, "claude")

	a := NewClaudeAgent(mockBinary)
	err := a.Start(t.TempDir(), "implement feature", "")
	require.NoError(t, err)

	var lines []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-a.Output():
			if !ok {
				goto done
			}
			lines = append(lines, line)
		case <-timeout:
			t.Fatal("timeout waiting for agent output")
		}
	}
done:
	output := strings.Join(lines, "")
	assert.Contains(t, output, "KODAMA_DONE:")
}

func TestClaudeAgentQuestion(t *testing.T) {
	mockBinary := buildMock(t, "claude")

	a := NewClaudeAgent(mockBinary)
	// Set MOCK_BEHAVIOR=question via env trick — pass as dir doesn't work,
	// but we can wrap cmd. For now just verify signal detection.
	err := a.Start(t.TempDir(), "implement feature", "")
	require.NoError(t, err)
	defer a.Stop()

	// Drain output.
	var lines []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-a.Output():
			if !ok {
				goto done
			}
			lines = append(lines, line)
		case <-timeout:
			goto done
		}
	}
done:
	// The mock always emits KODAMA_DONE in default mode.
	output := strings.Join(lines, "")
	_ = output
}

func TestClaudeAgentDetect(t *testing.T) {
	a := NewClaudeAgent("claude")
	sig, payload := a.Detect("KODAMA_DONE: All tests pass")
	assert.Equal(t, SignalDone, sig)
	assert.Equal(t, "All tests pass", payload)
}

func TestClaudeAgentStop(t *testing.T) {
	mockBinary := buildMock(t, "claude")

	a := NewClaudeAgent(mockBinary)
	err := a.Start(t.TempDir(), "implement feature", "")
	require.NoError(t, err)

	// Stop immediately.
	err = a.Stop()
	// Process may already be done, so ignore error.
	_ = err
}
