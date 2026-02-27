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
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	behavior := os.Getenv("MOCK_BEHAVIOR")

	// Extract task from args.
	var task string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") && args[i] != "exec" {
			task = args[i]
		}
	}

	switch behavior {
	case "question":
		fmt.Println("Starting codex task: " + task)
		fmt.Println("KODAMA_QUESTION: Which test framework should I use?")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			answer := scanner.Text()
			fmt.Println("Using: " + answer)
		}
		fmt.Println("KODAMA_DONE: Completed with selected framework")

	case "ratelimit":
		fmt.Println("Processing: " + task)
		time.Sleep(10 * time.Millisecond)
		fmt.Println("Rate limit exceeded. Too Many Requests.")
		fmt.Println("KODAMA_RATELIMIT: Rate limited")

	case "blocked":
		fmt.Println("KODAMA_BLOCKED: Missing required tool: eslint")

	default:
		fmt.Println("Codex processing: " + task)
		time.Sleep(10 * time.Millisecond)
		fmt.Println("Generating code...")
		fmt.Println("Writing tests...")
		fmt.Println("KODAMA_DONE: Code generation complete")
	}
}
