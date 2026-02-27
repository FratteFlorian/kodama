package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
)

// ClaudeAgent wraps the `claude` CLI subprocess.
type ClaudeAgent struct {
	binary string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string

	mu    sync.Mutex
	stdin io.WriteCloser // stdin pipe; prompt is written here, kept open for Q&A
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

// Start launches claude with the task, writing the prompt via stdin.
func (a *ClaudeAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Build the prompt: prepend "Read kodama.md" if contextFile is provided.
	prompt := task
	if contextFile != "" {
		prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
	}

	// Pass prompt via stdin, not as a positional arg — claude does not use positional args.
	args := []string{"--print", "--dangerously-skip-permissions"}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

	slog.Info("starting claude agent",
		"binary", a.binary,
		"workdir", workdir,
		"context_file", contextFile,
		"cmd", a.binary+" "+fmt.Sprintf("%v", args),
		"prompt_len", len(prompt),
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
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	a.stdin = stdinPipe

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start claude: %w", err)
	}
	a.cmd = cmd
	slog.Info("claude process started", "pid", cmd.Process.Pid)

	// Write the initial prompt to stdin. Do NOT close stdin so we can send
	// follow-up answers for KODAMA_QUESTION turns.
	go func() {
		slog.Info("writing prompt to claude stdin", "pid", cmd.Process.Pid, "prompt_len", len(prompt))
		if _, err := fmt.Fprintln(stdinPipe, prompt); err != nil {
			slog.Warn("write prompt to stdin", "pid", cmd.Process.Pid, "err", err)
		}
	}()

	// Stream stdout and stderr into output channel.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("claude stdout", "pid", cmd.Process.Pid, "line", line)
			a.output <- line + "\n"
		}
	}()
	go func() {
		defer wg.Done()
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

// Write sends input to the agent's stdin (e.g. an answer to a KODAMA_QUESTION).
func (a *ClaudeAgent) Write(input string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin == nil {
		return fmt.Errorf("agent not started")
	}
	_, err := fmt.Fprintln(a.stdin, input)
	return err
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
	a.mu.Lock()
	if a.stdin != nil {
		a.stdin.Close()
	}
	a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
