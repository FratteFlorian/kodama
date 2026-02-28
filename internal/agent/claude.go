package agent

import (
	"bufio"
	"context"
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
func (a *ClaudeAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Build the prompt: prepend "Read kodama.md" if contextFile is provided.
	prompt := task
	if contextFile != "" {
		prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
	}

	// Usage: claude [options] [prompt]
	// --print:                    non-interactive, print response and exit.
	// --dangerously-skip-permissions: bypass permission prompts.
	// --output-format stream-json: emit newline-delimited JSON events in real
	//   time (tool calls appear as they happen, not all at the end).
	// Prompt is the final positional argument.
	args := []string{
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		prompt,
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
		"context_file", contextFile,
		"args", fmt.Sprintf("--print --dangerously-skip-permissions --output-format stream-json <prompt(%d chars)>", len(prompt)),
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
