package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
)

// ClaudeAgent wraps the `claude` CLI subprocess.
type ClaudeAgent struct {
	binary string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string

	mu           sync.Mutex
	sessionID    string  // captured from stream-json events; used for --resume
	costUSD      float64 // from result event
	inputTokens  int64   // from result event
	outputTokens int64   // from result event
}

// NewClaudeAgent creates a new ClaudeAgent using the given binary path.
func NewClaudeAgent(binary string) *ClaudeAgent {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeAgent{
		binary: binary,
		output: make(chan string, 256),
	}
}

// Start launches claude with the task as a positional argument.
// Stdin is set to an empty reader so claude gets immediate EOF and doesn't block.
//
// If task starts with "RESUME:<sessionID>\n<answer>", claude is invoked with
// --resume <sessionID> to continue an existing conversation.
func (a *ClaudeAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	var args []string

	if strings.HasPrefix(task, "RESUME:") {
		// Resume an existing session: "RESUME:<sessionID>\n<answer>"
		rest := strings.TrimPrefix(task, "RESUME:")
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			cancel()
			return fmt.Errorf("malformed RESUME task: missing newline")
		}
		sessionID := rest[:idx]
		answer := rest[idx+1:]
		slog.Info("resuming claude session", "session_id", sessionID, "answer_len", len(answer))
		args = []string{
			"--print",
			"--verbose",
			"--dangerously-skip-permissions",
			"--output-format", "stream-json",
			"--resume", sessionID,
			answer,
		}
	} else {
		// Normal start: build prompt, optionally prepend context file instruction.
		prompt := task
		if contextFile != "" {
			prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
		}
		args = []string{
			"--print",
			"--verbose",
			"--dangerously-skip-permissions",
			"--output-format", "stream-json",
			prompt,
		}
	}

	cmd := exec.CommandContext(ctx, a.binary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// Give claude an empty stdin (immediate EOF) so it doesn't block waiting
	// for more input — an open pipe with no writer causes claude to hang.
	cmd.Stdin = strings.NewReader("")

	slog.Info("starting claude agent",
		"binary", a.binary,
		"workdir", workdir,
		"args_head", fmt.Sprintf("%v", args[:min(len(args), 5)]),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start claude: %w", err)
	}
	a.cmd = cmd
	slog.Info("claude process started", "pid", cmd.Process.Pid)

	// Stream stdout and stderr into output channel.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		// Increase buffer: stream-json lines can be large (file contents in tool results).
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			raw := scanner.Text()
			slog.Info("claude stdout", "pid", cmd.Process.Pid, "line", raw)

			// Capture session_id, cost and token usage from stream-json events.
			a.captureMetadata(raw)

			text := parseStreamLine(raw)
			if text != "" {
				a.output <- text
			}
		}
		if err := scanner.Err(); err != nil {
			slog.Warn("claude stdout scanner error", "pid", cmd.Process.Pid, "err", err)
		}
	}()
	go func() {
		defer wg.Done()
		// stderr is plain text (not JSON) — pass through directly.
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("claude stderr", "pid", cmd.Process.Pid, "line", line)
			a.output <- line + "\n"
		}
	}()

	// Close output channel when both streams end.
	go func() {
		wg.Wait()
		err := cmd.Wait()
		slog.Info("claude process exited", "pid", cmd.Process.Pid, "err", err)
		close(a.output)
	}()

	return nil
}

// captureMetadata extracts session_id, cost and token usage from stream-json events.
func (a *ClaudeAgent) captureMetadata(raw string) {
	var env struct {
		SessionID string  `json:"session_id"`
		Type      string  `json:"type"`
		CostUSD   float64 `json:"total_cost_usd"`
		Usage     struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionID == "" && env.SessionID != "" {
		a.sessionID = env.SessionID
		slog.Info("claude session ID captured", "session_id", env.SessionID)
	}
	// The result event carries final cost and token counts.
	if env.Type == "result" && env.CostUSD > 0 {
		a.costUSD = env.CostUSD
		a.inputTokens = env.Usage.InputTokens
		a.outputTokens = env.Usage.OutputTokens
		slog.Info("claude usage captured",
			"cost_usd", env.CostUSD,
			"input_tokens", env.Usage.InputTokens,
			"output_tokens", env.Usage.OutputTokens,
		)
	}
}

// SessionID returns the session ID captured from stream-json output.
// Used to resume the conversation with --resume after a KODAMA_QUESTION.
func (a *ClaudeAgent) SessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

// CostUSD returns the total API cost captured from the result event.
func (a *ClaudeAgent) CostUSD() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.costUSD
}

// TokensUsed returns the input and output token counts from the result event.
func (a *ClaudeAgent) TokensUsed() (int64, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inputTokens, a.outputTokens
}

// Write is not supported in --print mode (single-shot, no multi-turn).
// KODAMA_QUESTION answers would require restarting the process with conversation history.
func (a *ClaudeAgent) Write(input string) error {
	return fmt.Errorf("Write not supported in --print mode")
}

// Output returns the channel streaming agent output lines.
func (a *ClaudeAgent) Output() <-chan string {
	return a.output
}

// Detect parses a line for KODAMA_* signals.
func (a *ClaudeAgent) Detect(line string) (Signal, string) {
	return ParseSignal(line)
}

// Stop terminates the claude process.
func (a *ClaudeAgent) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
