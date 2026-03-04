package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/florian/kodama/internal/agent"
	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
	"github.com/florian/kodama/internal/telegram"
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
	cfg        *config.Config
	db         *db.DB
	hub        Broadcaster
	notifier   Notifier
	qa         QuestionAnswerer
	envManager *EnvironmentManager

	// Per-task question answer channels (used when Telegram is unavailable).
	questions   map[int64]chan string
	questionsMu sync.Mutex

	// Running project goroutines.
	projects   map[int64]context.CancelFunc
	projectsMu sync.Mutex

	tgMu     sync.Mutex
	tgBot    telegramClient
	tgCancel context.CancelFunc
	tgToken  string
	tgUserID int64

	agentFactory    func(task *db.Task, proj *db.Project) (agent.Agent, string)
	telegramFactory func(token string, userID int64) (telegramClient, error)
}

// New creates a new Daemon.
func New(cfg *config.Config, database *db.DB, hub Broadcaster, envHub Broadcaster) *Daemon {
	return &Daemon{
		cfg:        cfg,
		db:         database,
		hub:        hub,
		envManager: NewEnvironmentManager(database, envHub),
		questions:  make(map[int64]chan string),
		projects:   make(map[int64]context.CancelFunc),
	}
}

type telegramClient interface {
	Notifier
	QuestionAnswerer
	Start(ctx context.Context)
}

type telegramService struct {
	db     *db.DB
	daemon *Daemon
}

func (s telegramService) ListProjects() ([]telegram.ProjectInfo, error) {
	projects, err := s.db.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make([]telegram.ProjectInfo, 0, len(projects))
	for _, p := range projects {
		out = append(out, telegram.ProjectInfo{ID: p.ID, Name: p.Name})
	}
	return out, nil
}

func (s telegramService) ListTasks(projectID int64) ([]telegram.TaskInfo, error) {
	tasks, err := s.db.ListTasks(projectID)
	if err != nil {
		return nil, err
	}
	out := make([]telegram.TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, telegram.TaskInfo{
			ID:          t.ID,
			Status:      string(t.Status),
			Description: t.Description,
		})
	}
	return out, nil
}

func (s telegramService) CreateTask(projectID int64, description string) error {
	priority, err := s.db.NextTaskPriority(projectID)
	if err != nil {
		return err
	}
	_, err = s.db.CreateTask(projectID, description, "", priority, false)
	return err
}

func (s telegramService) StartProject(projectID int64) error {
	if s.daemon == nil {
		return fmt.Errorf("daemon not configured")
	}
	return s.daemon.StartProject(context.Background(), projectID)
}

// SetNotifier sets the notification backend (Telegram bot).
func (d *Daemon) SetNotifier(n Notifier) {
	d.notifier = n
}

// SetQuestionAnswerer sets the question answering backend (Telegram bot).
func (d *Daemon) SetQuestionAnswerer(qa QuestionAnswerer) {
	d.qa = qa
}

// SetAgentFactory sets a custom agent factory (used in tests).
func (d *Daemon) SetAgentFactory(factory func(task *db.Task, proj *db.Project) (agent.Agent, string)) {
	d.agentFactory = factory
}

// UpdateTelegramSettings configures the Telegram bot without restarting the daemon.
func (d *Daemon) UpdateTelegramSettings(token string, userID int64) error {
	d.tgMu.Lock()
	defer d.tgMu.Unlock()

	if d.telegramFactory == nil {
		d.telegramFactory = func(token string, userID int64) (telegramClient, error) {
			return telegram.New(token, userID, telegramService{db: d.db, daemon: d})
		}
	}

	if token == "" || userID == 0 {
		if d.tgCancel != nil {
			d.tgCancel()
		}
		d.tgCancel = nil
		d.tgBot = nil
		d.tgToken = ""
		d.tgUserID = 0
		d.SetNotifier(nil)
		d.SetQuestionAnswerer(nil)
		slog.Info("telegram disabled")
		return nil
	}

	if d.tgBot != nil && token == d.tgToken && userID == d.tgUserID {
		return nil
	}

	if d.tgCancel != nil {
		d.tgCancel()
	}

	bot, err := d.telegramFactory(token, userID)
	if err != nil {
		return fmt.Errorf("init telegram bot: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.tgCancel = cancel
	d.tgBot = bot
	d.tgToken = token
	d.tgUserID = userID

	d.SetNotifier(bot)
	d.SetQuestionAnswerer(bot)
	go bot.Start(ctx)
	slog.Info("telegram bot started", "user_id", userID)
	return nil
}

// StartProject begins sequential processing of a project's backlog.
func (d *Daemon) StartProject(ctx context.Context, projectID int64) error {
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()

	if _, running := d.projects[projectID]; running {
		slog.Warn("project already running", "project_id", projectID)
		return fmt.Errorf("project %d is already running", projectID)
	}
	slog.Info("starting project backlog", "project_id", projectID)

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
		slog.Info("stopping project", "project_id", projectID)
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
	defer d.stopProjectRuntime(projectID)
	projectName := ""
	if proj, err := d.db.GetProject(projectID); err == nil && proj != nil {
		projectName = proj.Name
	}
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
			d.sendNotification(formatTaskMsg(projectName, 0, "all tasks completed"))
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

	if ok {
		// If a stale value is buffered, drop it and prefer the latest user answer.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- answer:
			return nil
		case <-time.After(2 * time.Second):
			slog.Warn("question channel send timed out, falling back to persisted resume", "task_id", taskID)
		}
	}

	// Fallback path: if the daemon was restarted while the task was waiting,
	// there is no in-memory question channel anymore. Persist the answer so the
	// task can resume on the next run loop.
	task, err := d.db.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("no waiting question for task %d", taskID)
	}
	if task.Status != db.TaskStatusWaiting {
		return fmt.Errorf("no waiting question for task %d", taskID)
	}

	question := task.ResumeQuestion
	if question == "" {
		question = "question"
	}
	if err := d.db.UpdateTaskResume(taskID, question, answer); err != nil {
		return fmt.Errorf("store resume answer: %w", err)
	}
	if err := d.db.UpdateTaskStatus(taskID, db.TaskStatusPending); err != nil {
		return fmt.Errorf("set task pending: %w", err)
	}
	chunk := fmt.Sprintf("[User answered: %s]\n", answer)
	d.db.AppendTaskLog(taskID, chunk)
	if d.hub != nil {
		d.hub.Broadcast(taskID, chunk)
	}
	if !d.IsRunning(task.ProjectID) {
		if err := d.StartProject(context.Background(), task.ProjectID); err != nil {
			return fmt.Errorf("resume task: %w", err)
		}
	}
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
	if d.agentFactory != nil {
		return d.agentFactory(task, proj)
	}
	agentName := task.Agent
	if agentName == "" {
		agentName = proj.Agent
	}
	if agentName == "" {
		agentName = "codex"
	}

	var ag agent.Agent
	switch agentName {
	case "codex":
		ag = agent.NewCodexAgent("codex")
		slog.Info("selected agent", "agent", "codex", "task_id", task.ID)
	default:
		ag = agent.NewClaudeAgent(d.cfg.Claude.Binary)
		slog.Info("selected agent", "agent", "claude", "binary", d.cfg.Claude.Binary, "task_id", task.ID)
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

// StartEnvironment starts the dev environment for a project.
func (d *Daemon) StartEnvironment(ctx context.Context, projectID int64) error {
	env, err := d.db.GetEnvironment(projectID)
	if err != nil {
		return fmt.Errorf("get environment: %w", err)
	}
	if env == nil {
		return fmt.Errorf("no environment configured for project %d", projectID)
	}
	proj, err := d.db.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	return d.envManager.Start(ctx, env, proj.RepoPath)
}

// StopEnvironment stops the dev environment for a project.
func (d *Daemon) StopEnvironment(projectID int64) error {
	env, err := d.db.GetEnvironment(projectID)
	if err != nil || env == nil {
		return fmt.Errorf("no environment for project %d", projectID)
	}
	return d.envManager.Stop(env.ID)
}

// RestartEnvironment stops the environment (waiting for teardown) then starts it again.
func (d *Daemon) RestartEnvironment(ctx context.Context, projectID int64) error {
	env, err := d.db.GetEnvironment(projectID)
	if err != nil || env == nil {
		return fmt.Errorf("no environment for project %d", projectID)
	}
	d.envManager.StopAndWait(env.ID)
	return d.StartEnvironment(ctx, projectID)
}

// IsEnvRunning returns whether a project's dev environment is currently running.
func (d *Daemon) IsEnvRunning(projectID int64) bool {
	env, err := d.db.GetEnvironment(projectID)
	if err != nil || env == nil {
		return false
	}
	return d.envManager.IsRunning(env.ID)
}
