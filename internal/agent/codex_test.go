package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexAgentBasic(t *testing.T) {
	mockBinary := buildMock(t, "codex")

	a := NewCodexAgent(mockBinary)
	err := a.Start(t.TempDir(), "generate tests", "")
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
			t.Fatal("timeout waiting for codex output")
		}
	}
done:
	output := strings.Join(lines, "")
	assert.Contains(t, output, "KODAMA_DONE:")
	assert.Equal(t, "mock-codex-session-123", a.SessionID())
}

func TestCodexAgentDetect(t *testing.T) {
	a := NewCodexAgent("codex")
	sig, payload := a.Detect("KODAMA_BLOCKED: Cannot find dependency")
	assert.Equal(t, SignalBlocked, sig)
	assert.Equal(t, "Cannot find dependency", payload)
}

func TestCodexAgentResumeSession(t *testing.T) {
	mockBinary := buildMock(t, "codex")

	a := NewCodexAgent(mockBinary)
	err := a.Start(t.TempDir(), "RESUME:abc-123\ncontinue with tests", "")
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
			t.Fatal("timeout waiting for codex output")
		}
	}
done:
	output := strings.Join(lines, "")
	assert.Contains(t, output, "Codex resuming:")
	assert.Contains(t, output, "KODAMA_DONE:")
	assert.Equal(t, "abc-123", a.SessionID())
}

func TestCodexAgentCaptureTokenCountJSON(t *testing.T) {
	a := NewCodexAgent("codex")
	a.captureTokens(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1234,"output_tokens":56}}}}`)
	in, out := a.TokensUsed()
	assert.Equal(t, int64(1234), in)
	assert.Equal(t, int64(56), out)
}

func TestCodexAgentCaptureTokenCountTurnCompleted(t *testing.T) {
	a := NewCodexAgent("codex")
	a.captureTokens(`{"type":"turn.completed","usage":{"input_tokens":111,"cached_input_tokens":100,"output_tokens":22}}`)
	in, out := a.TokensUsed()
	assert.Equal(t, int64(11), in)
	assert.Equal(t, int64(22), out)
}
