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

	ag, agentName := d.newAgent(task, proj)
	entry := agentEntry{ag: ag, name: agentName}

	contextFile := kodamaMdPath(proj)
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
		questionTimeout = 30 * time.Second
	}

	// Process output.
	timer := time.NewTimer(questionTimeout)
	defer timer.Stop()

	done := false
	for !done {
		timer.Reset(questionTimeout)

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
			// Output timeout — treat as implicit question.
			slog.Info("output timeout, treating as implicit question", "task_id", task.ID)
			if err := d.handleQuestion(ctx, task, "Agent appears to be waiting for input.", ag); err != nil {
				d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
				done = true
			}
		}
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

// handleQuestion handles a KODAMA_QUESTION signal by notifying and waiting for answer.
func (d *Daemon) handleQuestion(ctx context.Context, task *db.Task, question string, ag agent.Agent) error {
	slog.Info("task waiting for input", "task_id", task.ID, "question", question)
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

	// Write answer to agent stdin.
	if err := ag.Write(answer); err != nil {
		slog.Error("write answer to agent", "err", err)
		return err
	}

	// Resume running status.
	d.db.UpdateTaskStatus(task.ID, db.TaskStatusRunning)
	// Log the answer as context.
	chunk := fmt.Sprintf("[User answered: %s]\n", answer)
	d.db.AppendTaskLog(task.ID, chunk)
	if d.hub != nil {
		d.hub.Broadcast(task.ID, chunk)
	}

	return nil
}
