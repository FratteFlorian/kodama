package agent

import "strings"

// rateLimitPatterns are native Claude Code rate limit message fragments.
var rateLimitPatterns = []string{
	"Claude AI usage limit reached",
	"rate limit",
	"Rate limit",
	"usage limit reached",
	"You have exceeded",
	"too many requests",
	"Too Many Requests",
}

// ParseSignal checks a line of output for KODAMA_* prefixes and native signals.
// It returns the detected Signal and the payload text after the prefix.
func ParseSignal(line string) (Signal, string) {
	trimmed := strings.TrimSpace(line)

	if payload, ok := stripPrefix(trimmed, "KODAMA_QUESTION:"); ok {
		return SignalQuestion, payload
	}
	if payload, ok := stripPrefix(trimmed, "KODAMA_DONE:"); ok {
		return SignalDone, payload
	}
	if payload, ok := stripPrefix(trimmed, "KODAMA_RATELIMIT:"); ok {
		return SignalRateLimited, payload
	}
	if payload, ok := stripPrefix(trimmed, "KODAMA_BLOCKED:"); ok {
		return SignalBlocked, payload
	}
	if payload, ok := stripPrefix(trimmed, "KODAMA_PR:"); ok {
		return SignalPR, payload
	}
	if payload, ok := stripPrefix(trimmed, "KODAMA_DECISION:"); ok {
		return SignalDecision, payload
	}

	// Detect native Claude Code rate limit messages.
	for _, pat := range rateLimitPatterns {
		if strings.Contains(line, pat) {
			return SignalRateLimited, line
		}
	}

	return SignalNone, ""
}

func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(s[len(prefix):]), true
	}
	return "", false
}
