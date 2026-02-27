package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/florian/kodama/internal/db"
)

const rateLimitDelay = 5 * time.Hour

// handleRateLimit saves a checkpoint and schedules retry.
func (d *Daemon) handleRateLimit(ctx context.Context, task *db.Task, lastOutput string, ag agentEntry) {
	checklist := ExtractChecklist(lastOutput)
	if checklist != "" {
		if err := d.db.SaveCheckpoint(task.ID, checklist); err != nil {
			slog.Error("save checkpoint", "task_id", task.ID, "err", err)
		}
	}

	if err := d.db.UpdateTaskStatus(task.ID, db.TaskStatusRateLimited); err != nil {
		slog.Error("update task status rate_limited", "task_id", task.ID, "err", err)
	}

	msg := fmt.Sprintf("Rate limit hit on task #%d: %s. Will retry in 5h.", task.ID, task.Description)
	d.sendNotification(msg)
	slog.Info("rate limit hit", "task_id", task.ID)

	// If failover is enabled, switch agent immediately.
	proj, err := d.db.GetProject(task.ProjectID)
	if err == nil && proj.Failover {
		slog.Info("YOLO failover: switching agent", "task_id", task.ID)
		altAgent := alternateAgent(ag.name, proj)
		if altAgent != ag.name {
			d.db.UpdateTaskAgent(task.ID, altAgent)
			d.db.UpdateTaskStatus(task.ID, db.TaskStatusPending)
			go d.processTask(ctx, task)
			return
		}
	}

	// Schedule retry after 5 hours.
	time.AfterFunc(rateLimitDelay, func() {
		if ctx.Err() != nil {
			return
		}
		slog.Info("resuming rate-limited task", "task_id", task.ID)
		d.sendNotification(fmt.Sprintf("Resuming task #%d: %s", task.ID, task.Description))
		freshTask, err := d.db.GetTask(task.ID)
		if err != nil {
			slog.Error("get task for resume", "err", err)
			return
		}
		freshTask.Status = db.TaskStatusPending
		go d.processTask(ctx, freshTask)
	})
}

// alternateAgent returns the other agent name for YOLO failover.
func alternateAgent(current string, proj *db.Project) string {
	if current == "claude" {
		return "codex"
	}
	if current == "codex" {
		return "claude"
	}
	// Fall back to project default.
	return proj.Agent
}
