// Mock claude binary for testing.
// Reads task from args and emits KODAMA_* signals based on MOCK_BEHAVIOR env var.
//
// Usage:
//
//	claude --print --dangerously-skip-permissions "task description"
//
// MOCK_BEHAVIOR:
//
//	(empty)    — emit some output then KODAMA_DONE
//	question   — emit output, then KODAMA_QUESTION, read answer, then KODAMA_DONE
//	ratelimit  — emit output, then KODAMA_RATELIMIT
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

	// Extract the task from args (last non-flag arg).
	var task string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			task = args[i]
		}
	}

	switch behavior {
	case "question":
		fmt.Println("Starting work on: " + task)
		time.Sleep(10 * time.Millisecond)
		fmt.Println("I need some clarification.")
		fmt.Println("KODAMA_QUESTION: Should I use PostgreSQL or SQLite?")
		// Read answer from stdin.
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			answer := scanner.Text()
			fmt.Println("Got answer: " + answer)
		}
		fmt.Println("KODAMA_DONE: Task completed with answer")

	case "ratelimit":
		fmt.Println("Starting work on: " + task)
		time.Sleep(10 * time.Millisecond)
		fmt.Println("- [ ] Step 1: Initialize")
		fmt.Println("- [x] Step 2: Analysis")
		fmt.Println("Claude AI usage limit reached. Please wait.")
		fmt.Println("KODAMA_RATELIMIT: Usage limit hit")

	case "blocked":
		fmt.Println("KODAMA_BLOCKED: Cannot find required environment variable DATABASE_URL")

	case "decision":
		fmt.Println("Analyzing options...")
		fmt.Println("KODAMA_DECISION: Using Chi router for HTTP layer")
		fmt.Println("KODAMA_DECISION: Using modernc.org/sqlite for database")
		fmt.Println("KODAMA_DONE: Architecture decided")

	default:
		// Normal completion.
		fmt.Println("Starting work on: " + task)
		time.Sleep(10 * time.Millisecond)
		fmt.Println("Analyzing the codebase...")
		fmt.Println("Implementing changes...")
		fmt.Println("Running tests...")
		fmt.Println("All tests pass!")
		fmt.Println("KODAMA_DONE: Task completed successfully")
	}
}
