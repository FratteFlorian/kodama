package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CodexAgent wraps the `codex` CLI subprocess.
type CodexAgent struct {
	binary string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string

	mu              sync.Mutex
	stdin2          interface{ Write([]byte) (int, error) }
	sessionID       string
	runWorkdir      string
	runStarted      time.Time
	lastErr         error
	inputTokens     int64
	outputTokens    int64
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
	a.mu.Lock()
	a.runWorkdir = workdir
	a.runStarted = time.Now()
	a.mu.Unlock()

	var args []string
	if strings.HasPrefix(task, "RESUME:") {
		rest := strings.TrimPrefix(task, "RESUME:")
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			cancel()
			return fmt.Errorf("malformed RESUME task: missing newline")
		}
		sessionID := rest[:idx]
		answer := rest[idx+1:]
		a.mu.Lock()
		a.sessionID = sessionID
		a.mu.Unlock()
		slog.Info("resuming codex session", "session_id", sessionID, "answer_len", len(answer))
		args = []string{
			"exec", "resume",
			"--full-auto",
			"--skip-git-repo-check",
			"--json",
			sessionID,
			answer,
		}
	} else {
		prompt := task
		if contextFile != "" {
			prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
		}
		// codex exec --full-auto runs non-interactively.
		// --skip-git-repo-check allows running outside a git repo.
		// --json emits machine-readable events that include session metadata.
		args = []string{"exec", "--full-auto", "--skip-git-repo-check", "--json", prompt}
	}
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
		// Codex --json can emit very large single-line events (e.g. session_meta).
		scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("codex stdout", "pid", cmd.Process.Pid, "line", line)
			a.captureTokens(line)
			a.captureSessionID(line)
			text := parseCodexJSONLine(line)
			if text != "" {
				a.output <- text
			}
		}
		if err := scanner.Err(); err != nil {
			slog.Warn("codex stdout scanner error", "pid", cmd.Process.Pid, "err", err)
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			slog.Info("codex stderr", "pid", cmd.Process.Pid, "line", line)
			a.captureTokens(line)
			a.output <- line + "\n"
		}
		if err := scanner.Err(); err != nil {
			slog.Warn("codex stderr scanner error", "pid", cmd.Process.Pid, "err", err)
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

// SessionID returns the session ID captured from codex --json session_meta events.
func (a *CodexAgent) SessionID() string {
	a.mu.Lock()
	sid := a.sessionID
	workdir := a.runWorkdir
	started := a.runStarted
	a.mu.Unlock()
	if sid != "" {
		return sid
	}
	// Fallback for codex versions/run modes that do not emit session_meta on stdout.
	if inferred := findRecentCodexSessionID(workdir, started); inferred != "" {
		a.mu.Lock()
		if a.sessionID == "" {
			a.sessionID = inferred
			slog.Info("codex session ID inferred from local session files", "session_id", inferred)
		}
		sid = a.sessionID
		a.mu.Unlock()
	}
	return sid
}

// CostUSD returns 0 — codex CLI events do not expose cost in current integration.
func (a *CodexAgent) CostUSD() float64 { return 0 }

// TokensUsed returns total token usage if captured from codex output.
func (a *CodexAgent) TokensUsed() (int64, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inputTokens > 0 || a.outputTokens > 0 {
		return a.inputTokens, a.outputTokens
	}
	return 0, a.tokenTotal
}

var tokensUsedRe = regexp.MustCompile(`(?i)tokens?\s+used[:\s]+([0-9][0-9.,]*)`)

func (a *CodexAgent) captureTokens(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if in, out, ok := parseCodexTokenCount(line); ok {
		a.inputTokens = in
		a.outputTokens = out
		if in > 0 || out > 0 {
			a.tokenTotal = in + out
		}
		return
	}

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

func parseCodexTokenCount(line string) (int64, int64, bool) {
	var ev struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info struct {
				Total struct {
					InputTokens  int64 `json:"input_tokens"`
					CachedInput  int64 `json:"cached_input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"total_token_usage"`
				Last struct {
					InputTokens  int64 `json:"input_tokens"`
					CachedInput  int64 `json:"cached_input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return 0, 0, false
	}
	if ev.Type == "turn.completed" {
		var turn struct {
			Type  string `json:"type"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				CachedInput  int64 `json:"cached_input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &turn); err != nil {
			return 0, 0, false
		}
		if turn.Usage.InputTokens > 0 || turn.Usage.OutputTokens > 0 {
			return normalizeInputTokens(turn.Usage.InputTokens, turn.Usage.CachedInput), turn.Usage.OutputTokens, true
		}
		return 0, 0, false
	}

	if ev.Type != "event_msg" || ev.Payload.Type != "token_count" {
		return 0, 0, false
	}
	if ev.Payload.Info.Total.InputTokens > 0 || ev.Payload.Info.Total.OutputTokens > 0 {
		return normalizeInputTokens(ev.Payload.Info.Total.InputTokens, ev.Payload.Info.Total.CachedInput), ev.Payload.Info.Total.OutputTokens, true
	}
	if ev.Payload.Info.Last.InputTokens > 0 || ev.Payload.Info.Last.OutputTokens > 0 {
		return normalizeInputTokens(ev.Payload.Info.Last.InputTokens, ev.Payload.Info.Last.CachedInput), ev.Payload.Info.Last.OutputTokens, true
	}
	return 0, 0, false
}

func normalizeInputTokens(input, cached int64) int64 {
	if cached <= 0 {
		return input
	}
	effective := input - cached
	if effective < 0 {
		return 0
	}
	return effective
}

func findRecentCodexSessionID(workdir string, started time.Time) string {
	root := codexSessionsRoot()
	if root == "" {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	candidates := make([]candidate, 0, 64)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Keep a narrow window around this run to avoid picking unrelated sessions.
		if !started.IsZero() && info.ModTime().Before(started.Add(-10*time.Minute)) {
			return nil
		}
		candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		return nil
	})
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})
	limit := len(candidates)
	if limit > 30 {
		limit = 30
	}
	for i := 0; i < limit; i++ {
		if sid := readSessionMetaIDForCWD(candidates[i].path, workdir); sid != "" {
			return sid
		}
	}
	return ""
}

func codexSessionsRoot() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return filepath.Join(v, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func readSessionMetaIDForCWD(path, workdir string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	rd := bufio.NewReader(f)
	line, err := rd.ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	var ev struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
		return ""
	}
	if ev.Type != "session_meta" || ev.Payload.ID == "" {
		return ""
	}
	if strings.TrimSpace(workdir) == "" || ev.Payload.CWD == workdir {
		return ev.Payload.ID
	}
	return ""
}

func (a *CodexAgent) captureSessionID(line string) {
	id := extractCodexSessionID(line)
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionID == "" {
		a.sessionID = id
		slog.Info("codex session ID captured", "session_id", id)
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
