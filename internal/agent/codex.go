package agent

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

// CodexAgent wraps the `codex` CLI subprocess.
type CodexAgent struct {
	binary string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string

	mu     sync.Mutex
	stdin2 interface{ Write([]byte) (int, error) }
}

// NewCodexAgent creates a new CodexAgent using the given binary path.
func NewCodexAgent(binary string) *CodexAgent {
	if binary == "" {
		binary = "codex"
	}
	return &CodexAgent{
		binary: binary,
		output: make(chan string, 256),
	}
}

// Start launches codex with the task in full-auto mode.
func (a *CodexAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	prompt := task
	if contextFile != "" {
		prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
	}

	// codex exec --full-auto runs non-interactively.
	args := []string{"exec", "--full-auto", prompt}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

	slog.Info("starting codex agent", "binary", a.binary, "workdir", workdir)

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
	a.stdin2 = stdinPipe

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start codex: %w", err)
	}
	a.cmd = cmd
	slog.Info("codex process started", "pid", cmd.Process.Pid)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("codex stdout", "pid", cmd.Process.Pid, "line", line)
			a.output <- line + "\n"
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("codex stderr", "pid", cmd.Process.Pid, "line", line)
			a.output <- line + "\n"
		}
	}()

	go func() {
		wg.Wait()
		err := cmd.Wait()
		slog.Info("codex process exited", "pid", cmd.Process.Pid, "err", err)
		close(a.output)
	}()

	return nil
}

// Write sends input to codex stdin.
func (a *CodexAgent) Write(input string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin2 == nil {
		return fmt.Errorf("agent not started")
	}
	_, err := a.stdin2.Write([]byte(input + "\n"))
	return err
}

// Output returns the channel streaming agent output lines.
func (a *CodexAgent) Output() <-chan string {
	return a.output
}

// Detect parses a line for KODAMA_* signals.
func (a *CodexAgent) Detect(line string) (Signal, string) {
	return ParseSignal(line)
}

// Stop terminates the codex process.
func (a *CodexAgent) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
