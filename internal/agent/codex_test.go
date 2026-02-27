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
}

func TestCodexAgentDetect(t *testing.T) {
	a := NewCodexAgent("codex")
	sig, payload := a.Detect("KODAMA_BLOCKED: Cannot find dependency")
	assert.Equal(t, SignalBlocked, sig)
	assert.Equal(t, "Cannot find dependency", payload)
}
