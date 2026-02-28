package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/florian/kodama/internal/agent"
	"github.com/florian/kodama/internal/db"
)

// processTask runs a single task to completion.
func (d *Daemon) processTask(ctx context.Context, task *db.Task) {
	start := time.Now()
	slog.Info("processing task", "task_id", task.ID, "desc", task.Description, "status", task.Status)
	defer func() {
		slog.Info("task finished", "task_id", task.ID, "elapsed", time.Since(start).Round(time.Millisecond))
	}()

	proj, err := d.db.GetProject(task.ProjectID)
	if err != nil {
		slog.Error("get project", "task_id", task.ID, "err", err)
		d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
		return
	}

	// Update status to running.
	if err := d.db.UpdateTaskStatus(task.ID, db.TaskStatusRunning); err != nil {
		slog.Error("update task status running", "task_id", task.ID, "err", err)
		return
	}

	// Build task prompt, prepending checkpoint if resuming.
	prompt := task.Description
	if task.Status == db.TaskStatusRateLimited {
		cp, err := d.db.GetLatestCheckpoint(task.ID)
		if err == nil && cp != nil {
			prompt = fmt.Sprintf(
				"Resume the following task. Previous progress checklist:\n%s\n\nTask: %s",
				cp.ChecklistState, task.Description,
			)
		}
		d.db.UpdateTaskStatus(task.ID, db.TaskStatusRunning)
	}

	// Inject dev environment context if an environment is running for this project.
	if env := d.envManager.ActiveEnv(proj.ID); env != nil {
		prompt = injectEnvContext(prompt, env)
	}

	ag, agentName := d.newAgent(task, proj)
	entry := agentEntry{ag: ag, name: agentName}

	contextFile := kodamaMdPath(proj)

	// Write an initial "started" message to the DB log so the task page
	// isn't blank while claude is running (--print produces no output until done).
	startMsg := fmt.Sprintf("[agent %s started at %s — waiting for output...]\n", agentName, time.Now().Format("15:04:05"))
	d.db.AppendTaskLog(task.ID, startMsg)
	if d.hub != nil {
		d.hub.Broadcast(task.ID, startMsg)
	}

	if err := ag.Start(proj.RepoPath, prompt, contextFile); err != nil {
		slog.Error("start agent", "task_id", task.ID, "err", err)
		errMsg := fmt.Sprintf("[error] failed to start agent %q: %v\nMake sure the binary is installed and ANTHROPIC_API_KEY is set.\n", agentName, err)
		d.db.AppendTaskLog(task.ID, errMsg)
		if d.hub != nil {
			d.hub.Broadcast(task.ID, errMsg)
		}
		d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
		d.sendNotification(fmt.Sprintf("Task #%d failed to start: %s", task.ID, err))
		return
	}

	var outputBuf strings.Builder
	var decisions []string
	var doneSummary string
	questionTimeout := d.cfg.QuestionTimeout
	if questionTimeout == 0 {
		questionTimeout = 600 * time.Second
	}

	// Heartbeat goroutine: broadcasts elapsed time every 30s via WebSocket while
	// claude runs. NOT stored in DB — only visible on the live task page.
	// Stops when heartbeatDone is closed.
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		agentStart := time.Now()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				elapsed := time.Since(agentStart).Round(time.Second)
				msg := fmt.Sprintf("[still running... %s elapsed]\n", elapsed)
				if d.hub != nil {
					d.hub.Broadcast(task.ID, msg)
				}
				slog.Info("agent heartbeat", "task_id", task.ID, "elapsed", elapsed)
			}
		}
	}()

	// Process output.
	// The timer fires if no output is seen for questionTimeout — used to detect
	// when the agent is silently waiting for input (e.g. prompting without KODAMA_QUESTION).
	timer := time.NewTimer(questionTimeout)
	defer timer.Stop()

	slog.Info("waiting for agent output", "task_id", task.ID, "timeout", questionTimeout)

	done := false
	for !done {
		select {
		case <-ctx.Done():
			ag.Stop()
			d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
			return

		case line, ok := <-ag.Output():
			if !ok {
				// Channel closed — agent process exited.
				slog.Info("agent output channel closed", "task_id", task.ID)
				done = true
				break
			}

			// Reset timeout timer on new output.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(questionTimeout)

			// Store output.
			outputBuf.WriteString(line)
			d.db.AppendTaskLog(task.ID, line)
			if d.hub != nil {
				d.hub.Broadcast(task.ID, line)
			}

			// Detect signals.
			sig, payload := ag.Detect(line)
			if sig != agent.SignalNone {
				slog.Info("signal detected", "task_id", task.ID, "signal", sig, "payload", payload)
			}
			switch sig {
			case agent.SignalQuestion:
				if err := d.handleQuestion(ctx, task, payload, ag); err != nil {
					slog.Error("handle question failed", "task_id", task.ID, "err", err)
					d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
					done = true
				} else {
					// Agent was stopped by handleQuestion; drain remaining output then
					// return so runProject re-runs the task with updated description.
					for range ag.Output() {
					}
					return
				}

			case agent.SignalDone:
				doneSummary = payload
				d.db.UpdateTaskStatus(task.ID, db.TaskStatusDone)
				d.sendNotification(fmt.Sprintf("Task #%d done: %s", task.ID, payload))
				slog.Info("task done", "task_id", task.ID, "summary", payload)
				done = true

			case agent.SignalRateLimited:
				slog.Warn("rate limit hit", "task_id", task.ID)
				ag.Stop()
				d.handleRateLimit(ctx, task, outputBuf.String(), entry)
				return

			case agent.SignalBlocked:
				d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
				d.sendNotification(fmt.Sprintf("Task #%d blocked: %s", task.ID, payload))
				slog.Warn("task blocked", "task_id", task.ID, "reason", payload)
				done = true

			case agent.SignalDecision:
				slog.Info("decision recorded", "task_id", task.ID, "decision", payload)
				decisions = append(decisions, payload)

			case agent.SignalPR:
				slog.Info("PR created", "task_id", task.ID, "url", payload)
				d.sendNotification(fmt.Sprintf("Task #%d PR: %s", task.ID, payload))
			}

		case <-timer.C:
			// No output for questionTimeout — treat as implicit question (agent may be waiting).
			slog.Warn("output timeout, treating as implicit question",
				"task_id", task.ID, "timeout", questionTimeout)
			if err := d.handleQuestion(ctx, task, "Agent appears to be waiting for input.", ag); err != nil {
				d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
				done = true
			}
			// Reset timer after handling the question so we don't immediately re-fire.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(questionTimeout)
		}
	}

	// Persist session ID and cost/tokens from the completed agent run.
	if sid := ag.SessionID(); sid != "" {
		d.db.UpdateTaskSessionID(task.ID, sid)
	}
	if cost := ag.CostUSD(); cost > 0 {
		in, out := ag.TokensUsed()
		d.db.UpdateTaskCost(task.ID, cost, in, out)
	}

	// Post-task: update kodama.md with decisions and status.
	if proj.RepoPath != "" {
		output := outputBuf.String()
		allDecisions := append(decisions, ExtractDecisions(output)...)
		if finalSummary := doneSummary; finalSummary == "" {
			finalSummary = ExtractDoneSummary(output)
		}
		if err := UpdateKodamaMd(proj.RepoPath, allDecisions, doneSummary); err != nil {
			slog.Warn("update kodama.md", "err", err)
		}
	}
}

// handleQuestion handles a KODAMA_QUESTION signal by notifying the user, waiting
// for an answer, then storing the session ID + answer so the caller can resume
// the exact claude session rather than starting from scratch.
func (d *Daemon) handleQuestion(ctx context.Context, task *db.Task, question string, ag agent.Agent) error {
	slog.Info("task waiting for input", "task_id", task.ID, "question", question)

	// Capture session ID and cost before stopping the agent.
	sessionID := ag.SessionID()
	slog.Info("captured session for resume", "task_id", task.ID, "session_id", sessionID)

	if sessionID != "" {
		d.db.UpdateTaskSessionID(task.ID, sessionID)
	}
	if cost := ag.CostUSD(); cost > 0 {
		in, out := ag.TokensUsed()
		d.db.UpdateTaskCost(task.ID, cost, in, out)
	}

	// Stop the current process — it has already completed its turn.
	ag.Stop()

	d.db.UpdateTaskStatus(task.ID, db.TaskStatusWaiting)

	// Register a local answer channel (for web UI).
	localCh := d.registerQuestion(task.ID)
	defer d.unregisterQuestion(task.ID)

	// Send Telegram notification if available.
	var telegramCh <-chan string
	if d.qa != nil {
		var err error
		telegramCh, err = d.qa.SendQuestion(task.ID, question)
		if err != nil {
			slog.Warn("send telegram question", "err", err)
		}
	}

	d.sendNotification(fmt.Sprintf("Task #%d waiting: %s", task.ID, question))

	// Wait for an answer from either source.
	var answer string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ans := <-localCh:
		answer = ans
	case ans, ok := <-telegramCh:
		if ok {
			answer = ans
		}
	}

	// Log the answer.
	chunk := fmt.Sprintf("[User answered: %s]\n", answer)
	d.db.AppendTaskLog(task.ID, chunk)
	if d.hub != nil {
		d.hub.Broadcast(task.ID, chunk)
	}

	if sessionID != "" {
		// Best path: resume the exact claude session. Store session ID in the
		// task description as a special prefix so newAgent/Start can pick it up.
		// Format: "RESUME:<sessionID>\n<answer>"
		d.db.UpdateTaskDescription(task.ID, fmt.Sprintf("RESUME:%s\n%s", sessionID, answer))
		slog.Info("will resume claude session", "task_id", task.ID, "session_id", sessionID)
	} else {
		// Fallback (e.g. codex): embed Q&A context in the prompt.
		prior, _ := d.db.GetFullLog(task.ID)
		newPrompt := fmt.Sprintf(
			"%s\n\nPrevious progress:\n%s\nAnswer to %q: %s",
			task.Description, prior, question, answer,
		)
		d.db.UpdateTaskDescription(task.ID, newPrompt)
		slog.Info("no session ID, falling back to context injection", "task_id", task.ID)
	}

	// Reset to pending so runProject picks it up on the next loop iteration.
	d.db.UpdateTaskStatus(task.ID, db.TaskStatusPending)
	return nil
}
