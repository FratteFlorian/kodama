package agent

import (
	"bufio"
	"context"
	"fmt"
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
	stdin  *strings.Reader // not used in --print mode

	mu     sync.Mutex
	stdin2 interface{ Write([]byte) (int, error) } // actual stdin pipe
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

// Start launches claude with the task using --print for non-interactive execution.
func (a *ClaudeAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Build the prompt: prepend "Read kodama.md" if contextFile is provided.
	prompt := task
	if contextFile != "" {
		prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
	}

	args := []string{"--print", "--dangerously-skip-permissions", prompt}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

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
		return fmt.Errorf("start claude: %w", err)
	}
	a.cmd = cmd

	// Stream stdout and stderr into output channel.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			a.output <- scanner.Text() + "\n"
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.output <- scanner.Text() + "\n"
		}
	}()

	// Close output channel when both streams end.
	go func() {
		wg.Wait()
		cmd.Wait()
		close(a.output)
	}()

	return nil
}

// Write sends input to the agent's stdin.
func (a *ClaudeAgent) Write(input string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin2 == nil {
		return fmt.Errorf("agent not started")
	}
	_, err := a.stdin2.Write([]byte(input + "\n"))
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
	if a.cancel != nil {
		a.cancel()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
