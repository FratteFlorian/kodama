// Mock codex binary for testing.
// Reads task from args and emits KODAMA_* signals based on MOCK_BEHAVIOR env var.
//
// Usage:
//
//	codex exec --full-auto "task description"
//
// MOCK_BEHAVIOR:
//
//	(empty)    — emit some output then KODAMA_DONE
//	question   — emit KODAMA_QUESTION, wait for answer, then KODAMA_DONE
//	ratelimit  — emit KODAMA_RATELIMIT
//	blocked    — emit KODAMA_BLOCKED
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	behavior := os.Getenv("MOCK_BEHAVIOR")
	jsonMode := false
	resumeMode := false

	// Extract task from args.
	var task string
	var sessionID string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--json" {
			jsonMode = true
		}
		if args[i] == "resume" {
			resumeMode = true
		}
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "resume" && i+1 < len(args) {
			j := i + 1
			for j < len(args) && strings.HasPrefix(args[j], "-") {
				j++
			}
			if j < len(args) {
				sessionID = args[j]
			}
		}
		if !strings.HasPrefix(args[i], "-") && args[i] != "exec" {
			task = args[i]
		}
	}
	if sessionID == "" {
		sessionID = "mock-codex-session-123"
	}
	if jsonMode {
		emitJSON("session_meta", map[string]any{"id": sessionID})
	}

	switch behavior {
	case "question":
		emitLine(jsonMode, "Starting codex task: "+task)
		emitLine(jsonMode, "KODAMA_QUESTION: Which test framework should I use?")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			answer := scanner.Text()
			emitLine(jsonMode, "Using: "+answer)
		}
		emitLine(jsonMode, "KODAMA_DONE: Completed with selected framework")

	case "ratelimit":
		emitLine(jsonMode, "Processing: "+task)
		time.Sleep(10 * time.Millisecond)
		emitLine(jsonMode, "Rate limit exceeded. Too Many Requests.")
		emitLine(jsonMode, "KODAMA_RATELIMIT: Rate limited")

	case "blocked":
		emitLine(jsonMode, "KODAMA_BLOCKED: Missing required tool: eslint")

	default:
		prefix := "Codex processing: "
		if resumeMode {
			prefix = "Codex resuming: "
		}
		emitLine(jsonMode, prefix+task)
		time.Sleep(10 * time.Millisecond)
		emitLine(jsonMode, "Generating code...")
		emitLine(jsonMode, "Writing tests...")
		emitLine(jsonMode, "KODAMA_DONE: Code generation complete")
	}
}

func emitLine(jsonMode bool, line string) {
	if !jsonMode {
		fmt.Println(line)
		return
	}
	emitJSON("event_msg", map[string]any{
		"type":    "agent_message",
		"message": line,
	})
}

func emitJSON(eventType string, payload map[string]any) {
	out, _ := json.Marshal(map[string]any{
		"type":    eventType,
		"payload": payload,
	})
	fmt.Println(string(out))
}
