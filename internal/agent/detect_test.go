package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSignal(t *testing.T) {
	tests := []struct {
		line    string
		signal  Signal
		payload string
	}{
		{"KODAMA_QUESTION: Should I use Postgres?", SignalQuestion, "Should I use Postgres?"},
		{"KODAMA_DONE: Task completed successfully", SignalDone, "Task completed successfully"},
		{"KODAMA_RATELIMIT: Limit reached", SignalRateLimited, "Limit reached"},
		{"KODAMA_BLOCKED: Missing env var", SignalBlocked, "Missing env var"},
		{"KODAMA_PR: https://github.com/user/repo/pull/42", SignalPR, "https://github.com/user/repo/pull/42"},
		{"KODAMA_DECISION: Using Chi router", SignalDecision, "Using Chi router"},
		{"just normal output", SignalNone, ""},
		{"", SignalNone, ""},
		// Native rate limit detection
		{"Claude AI usage limit reached for claude-3-5-sonnet", SignalRateLimited, "Claude AI usage limit reached for claude-3-5-sonnet"},
		{"Error: rate limit exceeded", SignalRateLimited, "Error: rate limit exceeded"},
		// Leading/trailing whitespace stripped
		{"  KODAMA_DONE: done  ", SignalDone, "done"},
		// Prefix only
		{"KODAMA_QUESTION:", SignalQuestion, ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			sig, payload := ParseSignal(tt.line)
			assert.Equal(t, tt.signal, sig)
			assert.Equal(t, tt.payload, payload)
		})
	}
}
