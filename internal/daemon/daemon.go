package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/florian/kodama/internal/agent"
	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
)

// Broadcaster is the interface used by the daemon to broadcast output chunks.
// Implemented by web.Hub.
type Broadcaster interface {
	Broadcast(taskID int64, chunk string)
}

// Notifier is the interface for sending notifications (Telegram).
type Notifier interface {
	SendNotification(msg string)
}

// QuestionAnswerer receives answers for waiting tasks.
type QuestionAnswerer interface {
	SendQuestion(taskID int64, question string) (<-chan string, error)
}

// agentEntry holds runtime info about a running agent.
type agentEntry struct {
	ag   agent.Agent
	name string
}

// Daemon manages task queue processing and coordinates all subsystems.
type Daemon struct {
	cfg      *config.Config
	db       *db.DB
	hub      Broadcaster
	notifier Notifier
	qa       QuestionAnswerer

	// Per-task question answer channels (used when Telegram is unavailable).
	questions   map[int64]chan string
	questionsMu sync.Mutex

	// Running project goroutines.
	projects   map[int64]context.CancelFunc
	projectsMu sync.Mutex
}

// New creates a new Daemon.
func New(cfg *config.Config, database *db.DB, hub Broadcaster) *Daemon {
	return &Daemon{
		cfg:       cfg,
		db:        database,
		hub:       hub,
		questions: make(map[int64]chan string),
		projects:  make(map[int64]context.CancelFunc),
	}
}

// SetNotifier sets the notification backend (Telegram bot).
func (d *Daemon) SetNotifier(n Notifier) {
	d.notifier = n
}

// SetQuestionAnswerer sets the question answering backend (Telegram bot).
func (d *Daemon) SetQuestionAnswerer(qa QuestionAnswerer) {
	d.qa = qa
}

// StartProject begins sequential processing of a project's backlog.
func (d *Daemon) StartProject(ctx context.Context, projectID int64) error {
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()

	if _, running := d.projects[projectID]; running {
		return fmt.Errorf("project %d is already running", projectID)
	}

	projCtx, cancel := context.WithCancel(ctx)
	d.projects[projectID] = cancel

	go func() {
		defer func() {
			d.projectsMu.Lock()
			delete(d.projects, projectID)
			d.projectsMu.Unlock()
		}()
		d.runProject(projCtx, projectID)
	}()

	return nil
}

// StopProject cancels a running project.
func (d *Daemon) StopProject(projectID int64) {
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()
	if cancel, ok := d.projects[projectID]; ok {
		cancel()
	}
}

// IsRunning returns whether a project is currently running.
func (d *Daemon) IsRunning(projectID int64) bool {
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()
	_, ok := d.projects[projectID]
	return ok
}

// runProject processes all pending tasks for a project sequentially.
func (d *Daemon) runProject(ctx context.Context, projectID int64) {
	slog.Info("starting project", "project_id", projectID)
	for {
		if ctx.Err() != nil {
			return
		}

		tasks, err := d.db.ListPendingTasks(projectID)
		if err != nil {
			slog.Error("list pending tasks", "err", err)
			return
		}
		if len(tasks) == 0 {
			slog.Info("no more pending tasks", "project_id", projectID)
			return
		}

		task := tasks[0]
		d.processTask(ctx, task)

		if ctx.Err() != nil {
			return
		}
	}
}

// AnswerQuestion submits an answer for a waiting task (used by web UI).
func (d *Daemon) AnswerQuestion(taskID int64, answer string) error {
	d.questionsMu.Lock()
	ch, ok := d.questions[taskID]
	d.questionsMu.Unlock()

	if !ok {
		return fmt.Errorf("no waiting question for task %d", taskID)
	}
	ch <- answer
	return nil
}

// registerQuestion registers a channel to receive the answer for a task.
func (d *Daemon) registerQuestion(taskID int64) chan string {
	ch := make(chan string, 1)
	d.questionsMu.Lock()
	d.questions[taskID] = ch
	d.questionsMu.Unlock()
	return ch
}

// unregisterQuestion removes the question channel for a task.
func (d *Daemon) unregisterQuestion(taskID int64) {
	d.questionsMu.Lock()
	delete(d.questions, taskID)
	d.questionsMu.Unlock()
}

// sendNotification sends a notification if a notifier is configured.
func (d *Daemon) sendNotification(msg string) {
	if d.notifier != nil {
		d.notifier.SendNotification(msg)
	} else {
		slog.Info("notification", "msg", msg)
	}
}

// newAgent creates an agent for a task, respecting project config and Docker.
func (d *Daemon) newAgent(task *db.Task, proj *db.Project) (agent.Agent, string) {
	agentName := task.Agent
	if agentName == "" {
		agentName = proj.Agent
	}
	if agentName == "" {
		agentName = "claude"
	}

	var ag agent.Agent
	switch agentName {
	case "codex":
		ag = agent.NewCodexAgent("codex")
	default:
		ag = agent.NewClaudeAgent(d.cfg.Claude.Binary)
	}

	// Wrap in Docker if project has an image configured.
	if proj.DockerImage != "" {
		ag = agent.NewDockerAgent(ag, proj.DockerImage, proj.RepoPath)
	}

	return ag, agentName
}

// kodamaMdPath returns the path to kodama.md for a project.
func kodamaMdPath(proj *db.Project) string {
	if proj.RepoPath == "" {
		return ""
	}
	return filepath.Join(proj.RepoPath, "kodama.md")
}
