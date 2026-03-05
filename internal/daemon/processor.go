package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
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
	if err := d.ensureProjectRuntime(ctx, proj); err != nil {
		slog.Error("ensure project runtime", "task_id", task.ID, "project_id", proj.ID, "err", err)
		msg := fmt.Sprintf("[error] failed to prepare runtime: %v\n", err)
		d.db.AppendTaskLog(task.ID, msg)
		if d.hub != nil {
			d.hub.Broadcast(task.ID, msg)
		}
		d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
		return
	}

	// Update status to running.
	if err := d.db.UpdateTaskStatus(task.ID, db.TaskStatusRunning); err != nil {
		slog.Error("update task status running", "task_id", task.ID, "err", err)
		return
	}

	// Build task prompt, prepending checkpoint if resuming.
	baseTask := applyTaskProfile(task.Description, task.Profile)
	baseTask = withProtocolReminder(baseTask, proj)
	prompt := baseTask
	if task.Status == db.TaskStatusRateLimited {
		cp, err := d.db.GetLatestCheckpoint(task.ID)
		if err == nil && cp != nil {
			prompt = fmt.Sprintf(
				"Resume the following task. Previous progress checklist:\n%s\n\nTask: %s",
				cp.ChecklistState, baseTask,
			)
		}
		d.db.UpdateTaskStatus(task.ID, db.TaskStatusRunning)
	}

	// Resume a waiting conversation without mutating the original task description.
	if task.ResumeAnswer != "" {
		if task.SessionID != "" {
			prompt = fmt.Sprintf("RESUME:%s\n%s", task.SessionID, task.ResumeAnswer)
		} else {
			prior, _ := d.db.GetFullLog(task.ID)
			question := task.ResumeQuestion
			if question == "" {
				question = "question"
			}
			prompt = fmt.Sprintf(
				"%s\n\nPrevious progress:\n%s\nAnswer to %q: %s",
				baseTask, prior, question, task.ResumeAnswer,
			)
		}
		d.db.ClearTaskResume(task.ID)
	}
	// Follow-up task mode: start a new pending task by resuming a selected prior
	// session and sending the new task description as the next user message.
	if task.Status == db.TaskStatusPending &&
		task.SessionID != "" &&
		task.ResumeQuestion == "" &&
		task.ResumeAnswer == "" {
		prompt = fmt.Sprintf("RESUME:%s\n%s", task.SessionID, baseTask)
	}

	// Inject dev environment context if an environment is running for this project.
	if env := d.envManager.ActiveEnv(proj.ID); env != nil {
		prompt = injectEnvContext(prompt, env)
	}
	projectFiles, _ := d.db.ListProjectAttachments(proj.ID)
	taskFiles, _ := d.db.ListTaskAttachments(task.ID)
	prompt = injectAttachmentContext(prompt, projectFiles, taskFiles)

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
		d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("failed to start: %s", err)))
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
	finalised := false // true when an explicit terminal signal set the task status
	for !done {
		select {
		case <-ctx.Done():
			ag.Stop()
			d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
			finalised = true
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

			// Detect signals line-by-line. JSON mode may emit multi-line chunks.
			chunkFinalised := false
			for _, signalLine := range strings.Split(line, "\n") {
				if strings.TrimSpace(signalLine) == "" {
					continue
				}
				sig, payload := ag.Detect(signalLine)
				if sig == agent.SignalNone {
					continue
				}
				slog.Info("signal detected", "task_id", task.ID, "signal", sig, "payload", payload)
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
					chunkFinalised = true

				case agent.SignalDone:
					doneSummary = payload
					d.db.UpdateTaskStatus(task.ID, db.TaskStatusDone)
					d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("done: %s", payload)))
					slog.Info("task done", "task_id", task.ID, "summary", payload)
					finalised = true
					done = true
					chunkFinalised = true

				case agent.SignalRateLimited:
					slog.Warn("rate limit hit", "task_id", task.ID)
					ag.Stop()
					d.handleRateLimit(ctx, task, outputBuf.String(), entry)
					return

				case agent.SignalBlocked:
					d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
					d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("blocked: %s", payload)))
					slog.Warn("task blocked", "task_id", task.ID, "reason", payload)
					finalised = true
					done = true
					chunkFinalised = true

				case agent.SignalDecision:
					slog.Info("decision recorded", "task_id", task.ID, "decision", payload)
					decisions = append(decisions, payload)

				case agent.SignalPR:
					slog.Info("PR created", "task_id", task.ID, "url", payload)
					d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("PR: %s", payload)))
				}
				if chunkFinalised {
					break
				}
			}
			if chunkFinalised {
				continue
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

	// If the agent exited cleanly without an explicit terminal signal (e.g. no
	// KODAMA_DONE emitted), mark the task done now rather than leaving it stuck
	// in "running".
	if !finalised {
		if hasRateLimitSignal(outputBuf.String()) {
			slog.Warn("rate limit detected after exit", "task_id", task.ID)
			d.handleRateLimit(ctx, task, outputBuf.String(), entry)
			return
		}
		if err := ag.LastError(); err != nil {
			slog.Warn("agent exited with error", "task_id", task.ID, "err", err)
			d.db.UpdateTaskStatus(task.ID, db.TaskStatusFailed)
			d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("failed: %v", err)))
		} else {
			slog.Info("agent exited without signal, marking task done", "task_id", task.ID)
			d.db.UpdateTaskStatus(task.ID, db.TaskStatusDone)
			if doneSummary != "" {
				summary := trimNotification(doneSummary, 180)
				tail := trimNotification(outputTail(outputBuf.String(), 400), 400)
				if tail != "" {
					d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("done: %s\n\nLast output:\n%s", summary, tail)))
				} else {
					d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("done: %s", summary)))
				}
			} else {
				d.sendNotification(formatTaskMsg(proj.Name, task.ID, "done"))
			}
		}
	}

	// Drain any remaining output so captureMetadata has processed the result
	// event (which carries session ID, cost, and token counts) before we read them.
	for range ag.Output() {
	}

	// Persist session ID and cost/tokens from the completed agent run.
	if sid := ag.SessionID(); sid != "" {
		d.db.UpdateTaskSessionID(task.ID, sid)
	}
	cost := ag.CostUSD()
	in, out := ag.TokensUsed()
	if cost > 0 || in > 0 || out > 0 {
		d.db.UpdateTaskCost(task.ID, cost, in, out)
	}

	// Post-task: update kodama.md with decisions and status.
	output := outputBuf.String()
	if planned, err := extractPlannedTasks(output); err != nil {
		slog.Warn("parse planned tasks", "task_id", task.ID, "err", err)
	} else if len(planned) > 0 {
		imported, err := d.importPlannedTasks(task.ProjectID, planned)
		if err != nil {
			slog.Warn("import planned tasks", "task_id", task.ID, "err", err)
		} else if imported > 0 {
			msg := fmt.Sprintf("[imported %d planned tasks]\n", imported)
			d.db.AppendTaskLog(task.ID, msg)
			if d.hub != nil {
				d.hub.Broadcast(task.ID, msg)
			}
			d.sendNotification(formatTaskMsg(proj.Name, task.ID, fmt.Sprintf("imported %d planned tasks", imported)))
		}
	}

	// Post-task: update kodama.md with decisions and status.
	if proj.RepoPath != "" {
		allDecisions := append(decisions, ExtractDecisions(output)...)
		if finalSummary := doneSummary; finalSummary == "" {
			finalSummary = ExtractDoneSummary(output)
		}
		if err := UpdateKodamaMd(proj.RepoPath, allDecisions, doneSummary); err != nil {
			slog.Warn("update kodama.md", "err", err)
		}
	}
}

func withProtocolReminder(task string, proj *db.Project) string {
	kodamaPath := "kodama.md"
	if proj != nil && strings.TrimSpace(proj.RepoPath) != "" {
		kodamaPath = filepath.Join(proj.RepoPath, "kodama.md")
	}
	return fmt.Sprintf(
		"Read %s first and strictly follow its communication protocol.\n"+
			"Emit protocol markers exactly as defined there (e.g. KODAMA_QUESTION:, KODAMA_DONE:, KODAMA_RATELIMIT:, KODAMA_BLOCKED:, KODAMA_PR:, KODAMA_DECISION:).\n\n%s",
		kodamaPath, task,
	)
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
	d.db.UpdateTaskResume(task.ID, question, "")

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

	projectName := ""
	if proj, err := d.db.GetProject(task.ProjectID); err == nil && proj != nil {
		projectName = proj.Name
	}
	d.sendNotification(formatTaskMsg(projectName, task.ID, fmt.Sprintf("waiting: %s", question)))

	// Wait for an answer from either source and periodically remind when still waiting.
	var answer string
	reminderEvery := d.cfg.WaitingReminder
	if reminderEvery == 0 {
		reminderEvery = 30 * time.Minute
	}
	var reminderC <-chan time.Time
	var reminderTimer *time.Timer
	if reminderEvery > 0 {
		reminderTimer = time.NewTimer(reminderEvery)
		reminderC = reminderTimer.C
		defer reminderTimer.Stop()
	}
waitLoop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ans := <-localCh:
			answer = ans
			break waitLoop
		case ans, ok := <-telegramCh:
			if ok {
				answer = ans
				break waitLoop
			}
		case <-reminderC:
			reminder := fmt.Sprintf("[still waiting for input: %s]\n", question)
			d.db.AppendTaskLog(task.ID, reminder)
			if d.hub != nil {
				d.hub.Broadcast(task.ID, reminder)
			}
			d.sendNotification(formatTaskMsg(projectName, task.ID,
				fmt.Sprintf("still waiting for input: %s", trimNotification(question, 180))))
			if reminderTimer != nil {
				reminderTimer.Reset(reminderEvery)
			}
		}
	}

	// Log the answer.
	chunk := fmt.Sprintf("[User answered: %s]\n", answer)
	d.db.AppendTaskLog(task.ID, chunk)
	if d.hub != nil {
		d.hub.Broadcast(task.ID, chunk)
	}

	// Store resume context without mutating the task description.
	d.db.UpdateTaskResume(task.ID, question, answer)
	if sessionID != "" {
		slog.Info("will resume claude session", "task_id", task.ID, "session_id", sessionID)
	} else {
		slog.Info("no session ID, will resume via context injection", "task_id", task.ID)
	}

	// Reset to pending so runProject picks it up on the next loop iteration.
	d.db.UpdateTaskStatus(task.ID, db.TaskStatusPending)
	return nil
}

func hasRateLimitSignal(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if sig, _ := agent.ParseSignal(line); sig == agent.SignalRateLimited {
			return true
		}
	}
	return false
}

func trimNotification(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func outputTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[len(s)-max:])
}
