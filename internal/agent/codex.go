package agent

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// CodexAgent wraps the `codex` CLI subprocess.
type CodexAgent struct {
	binary string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string

	mu              sync.Mutex
	stdin2          interface{ Write([]byte) (int, error) }
	lastErr         error
	tokenTotal      int64
	expectTokenLine bool
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
	// --skip-git-repo-check allows running outside a git repo.
	args := []string{"exec", "--full-auto", "--skip-git-repo-check", prompt}
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
			a.captureTokens(line)
			a.output <- line + "\n"
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("codex stderr", "pid", cmd.Process.Pid, "line", line)
			a.captureTokens(line)
			a.output <- line + "\n"
		}
	}()

	go func() {
		wg.Wait()
		err := cmd.Wait()
		slog.Info("codex process exited", "pid", cmd.Process.Pid, "err", err)
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
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

// SessionID returns "" — codex does not support session resumption.
func (a *CodexAgent) SessionID() string { return "" }

// CostUSD returns 0 — codex does not report cost.
func (a *CodexAgent) CostUSD() float64 { return 0 }

// TokensUsed returns total token usage if captured from codex output.
func (a *CodexAgent) TokensUsed() (int64, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return 0, a.tokenTotal
}

var tokensUsedRe = regexp.MustCompile(`(?i)tokens?\s+used[:\s]+([0-9][0-9.,]*)`)

func (a *CodexAgent) captureTokens(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.expectTokenLine {
		if val, ok := parseTokenNumber(line); ok {
			a.tokenTotal = val
			a.expectTokenLine = false
			return
		}
	}

	if m := tokensUsedRe.FindStringSubmatch(line); len(m) == 2 {
		if val, ok := parseTokenNumber(m[1]); ok {
			a.tokenTotal = val
			return
		}
	}

	if strings.Contains(strings.ToLower(line), "tokens used") {
		a.expectTokenLine = true
	}
}

func parseTokenNumber(s string) (int64, bool) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, false
	}
	if sep := strings.LastIndexAny(raw, ".,"); sep != -1 && len(raw)-sep-1 == 3 {
		clean := strings.NewReplacer(".", "", ",", "").Replace(raw)
		if n, err := strconv.ParseInt(clean, 10, 64); err == nil {
			return n, true
		}
	}
	if f, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64); err == nil {
		if f < 0 {
			return 0, false
		}
		return int64(f + 0.5), true
	}
	return 0, false
}

// LastError returns the last process error after exit (if any).
func (a *CodexAgent) LastError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
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
