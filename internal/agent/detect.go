package agent

import "strings"

// ParseSignal checks a line of output for KODAMA_* prefixes.
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

	return SignalNone, ""
}

func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(s[len(prefix):]), true
	}
	return "", false
}
