package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florian/kodama/internal/agent"
	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
	"github.com/stretchr/testify/require"
)

type fakeAgent struct {
	output chan string
	sessID string
}

func newFakeAgent(lines ...string) *fakeAgent {
	ch := make(chan string, 16)
	go func() {
		for _, l := range lines {
			ch <- l + "\n"
		}
		close(ch)
	}()
	return &fakeAgent{output: ch}
}

func (f *fakeAgent) Start(workdir, task, contextFile string) error { return nil }
func (f *fakeAgent) Write(input string) error                      { return nil }
func (f *fakeAgent) Output() <-chan string                         { return f.output }
func (f *fakeAgent) Detect(line string) (agent.Signal, string)     { return agent.ParseSignal(line) }
func (f *fakeAgent) Stop() error                                   { return nil }
func (f *fakeAgent) SessionID() string                             { return f.sessID }
func (f *fakeAgent) CostUSD() float64                              { return 0 }
func (f *fakeAgent) TokensUsed() (int64, int64)                    { return 0, 0 }
func (f *fakeAgent) LastError() error                              { return nil }

type captureAgent struct {
	output chan string
	onTask func(string)
}

func newCaptureAgent(onTask func(string), lines ...string) *captureAgent {
	ch := make(chan string, 16)
	go func() {
		for _, l := range lines {
			ch <- l + "\n"
		}
		close(ch)
	}()
	return &captureAgent{output: ch, onTask: onTask}
}

func (f *captureAgent) Start(workdir, task, contextFile string) error {
	if f.onTask != nil {
		f.onTask(task)
	}
	return nil
}
func (f *captureAgent) Write(input string) error                  { return nil }
func (f *captureAgent) Output() <-chan string                     { return f.output }
func (f *captureAgent) Detect(line string) (agent.Signal, string) { return agent.ParseSignal(line) }
func (f *captureAgent) Stop() error                               { return nil }
func (f *captureAgent) SessionID() string                         { return "" }
func (f *captureAgent) CostUSD() float64                          { return 0 }
func (f *captureAgent) TokensUsed() (int64, int64)                { return 0, 0 }
func (f *captureAgent) LastError() error                          { return nil }

type captureNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (n *captureNotifier) SendNotification(msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, msg)
}

func (n *captureNotifier) countContaining(substr string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, m := range n.msgs {
		if strings.Contains(m, substr) {
			count++
		}
	}
	return count
}

func TestTaskLifecycleDone(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent("KODAMA_DONE: ok"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))

	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)
}

func TestTaskLifecycleDoneFromMultilineChunk(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		ch := make(chan string, 1)
		ch <- "progress line\nKODAMA_DONE: ok\n"
		close(ch)
		return &fakeAgent{output: ch}, "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)
}

func TestQuestionFlowSetsWaiting(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent("KODAMA_QUESTION: need input"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))

	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusWaiting
	}, 2*time.Second, 50*time.Millisecond)

	got, err := database.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, "need input", got.ResumeQuestion)
}

func TestAnswerQuestionFallbackWithoutLiveChannel(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskStatus(task.ID, db.TaskStatusWaiting))
	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent("KODAMA_DONE: resumed"), "codex"
	})

	require.NoError(t, d.AnswerQuestion(task.ID, "Use SQLite."))

	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	logs, err := database.GetFullLog(task.ID)
	require.NoError(t, err)
	require.Contains(t, logs, "[User answered: Use SQLite.]")
}

func TestAnswerQuestionAfterRestartResumesTask(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskStatus(task.ID, db.TaskStatusWaiting))
	require.NoError(t, database.UpdateTaskResume(task.ID, "choose db", ""))

	var mu sync.Mutex
	var gotPrompt string
	d2 := New(cfg, database, nil, nil)
	d2.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newCaptureAgent(func(p string) {
			mu.Lock()
			gotPrompt = p
			mu.Unlock()
		}, "KODAMA_DONE: resumed"), "codex"
	})

	require.NoError(t, d2.AnswerQuestion(task.ID, "Use SQLite."))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	require.Contains(t, prompt, "Use SQLite.")
	require.Contains(t, prompt, "Answer to")
}

func TestAnswerQuestionAfterRestartResumesWithSessionID(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskStatus(task.ID, db.TaskStatusWaiting))
	require.NoError(t, database.UpdateTaskResume(task.ID, "choose db", ""))
	require.NoError(t, database.UpdateTaskSessionID(task.ID, "codex-session-xyz"))

	var mu sync.Mutex
	var gotPrompt string
	d2 := New(cfg, database, nil, nil)
	d2.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newCaptureAgent(func(p string) {
			mu.Lock()
			gotPrompt = p
			mu.Unlock()
		}, "KODAMA_DONE: resumed"), "codex"
	})

	require.NoError(t, d2.AnswerQuestion(task.ID, "Use SQLite."))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	require.Equal(t, "RESUME:codex-session-xyz\nUse SQLite.", prompt)
}

func TestAnswerQuestionReplacesStaleBufferedChannelValue(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskStatus(task.ID, db.TaskStatusWaiting))
	require.NoError(t, database.UpdateTaskResume(task.ID, "choose db", ""))

	// Simulate a stale/full in-memory question channel.
	ch := d.registerQuestion(task.ID)
	ch <- "already queued"
	defer d.unregisterQuestion(task.ID)

	require.NoError(t, d.AnswerQuestion(task.ID, "Use SQLite."))

	received := <-ch
	require.Equal(t, "Use SQLite.", received)

	got, err := database.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, db.TaskStatusWaiting, got.Status)
	require.Equal(t, "", got.ResumeAnswer)
}

func TestWaitingReminderSendsEscalation(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{
		QuestionTimeout: 2 * time.Second,
		WaitingReminder: 100 * time.Millisecond,
	}
	d := New(cfg, database, nil, nil)
	n := &captureNotifier{}
	d.SetNotifier(n)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent("KODAMA_QUESTION: need input"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		return n.countContaining("still waiting for input") > 0
	}, 2*time.Second, 25*time.Millisecond)
	d.StopProject(proj.ID)
	require.Eventually(t, func() bool {
		return !d.IsRunning(proj.ID)
	}, 2*time.Second, 25*time.Millisecond)

	logs, err := database.GetFullLog(task.ID)
	require.NoError(t, err)
	require.Contains(t, logs, "[still waiting for input: need input]")
}

func TestTaskPromptIncludesAttachmentContext(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	tmp := t.TempDir()
	projectPath := filepath.Join(tmp, "project-spec.pdf")
	taskPath := filepath.Join(tmp, "screen.png")
	require.NoError(t, os.WriteFile(projectPath, []byte("p"), 0644))
	require.NoError(t, os.WriteFile(taskPath, []byte("t"), 0644))
	_, err = database.CreateProjectAttachment(proj.ID, "project-spec.pdf", projectPath, "application/pdf", 1)
	require.NoError(t, err)
	_, err = database.CreateTaskAttachment(task.ID, "screen.png", taskPath, "image/png", 1)
	require.NoError(t, err)

	var mu sync.Mutex
	var gotPrompt string
	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newCaptureAgent(func(p string) {
			mu.Lock()
			gotPrompt = p
			mu.Unlock()
		}, "KODAMA_DONE: ok"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	require.Contains(t, prompt, "Reference files are available for this task")
	require.Contains(t, prompt, projectPath)
	require.Contains(t, prompt, taskPath)
}

func TestTaskPromptIncludesProtocolReminder(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp/repo", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	var mu sync.Mutex
	var gotPrompt string
	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newCaptureAgent(func(p string) {
			mu.Lock()
			gotPrompt = p
			mu.Unlock()
		}, "KODAMA_DONE: ok"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	require.Contains(t, prompt, "Read /tmp/repo/kodama.md first")
	require.Contains(t, prompt, "KODAMA_QUESTION:")
	require.Contains(t, prompt, "KODAMA_DONE:")
}

func TestPendingTaskWithSessionIDStartsAsFollowupResume(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "continue with cleanup", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskSessionID(task.ID, "session-abc"))

	var mu sync.Mutex
	var gotPrompt string
	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newCaptureAgent(func(p string) {
			mu.Lock()
			gotPrompt = p
			mu.Unlock()
		}, "KODAMA_DONE: ok"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	require.Contains(t, prompt, "RESUME:session-abc\n")
	require.Contains(t, prompt, "Read /tmp/kodama.md first and strictly follow its communication protocol.")
	require.Contains(t, prompt, "\ncontinue with cleanup")
}

func TestPlannedTasksAreImportedFromOutput(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "plan backlog", "", 0, false)
	require.NoError(t, err)
	plannerID := task.ID

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		if task.ID == plannerID {
			return newFakeAgent(
				"KODAMA_TASKS_BEGIN",
				`[{"title":"Implement auth","description":"JWT login","priority":0,"profile":"developer","agent":"codex","failover":false}]`,
				"KODAMA_TASKS_END",
				"KODAMA_DONE: planned",
			), "codex"
		}
		return newFakeAgent("KODAMA_DONE: ok"), "codex"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))
	require.Eventually(t, func() bool {
		tasks, _ := database.ListTasks(proj.ID)
		return len(tasks) >= 2
	}, 2*time.Second, 50*time.Millisecond)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	var found bool
	for _, tsk := range tasks {
		if strings.Contains(tsk.Description, "Implement auth") {
			found = true
			require.Equal(t, "developer", tsk.Profile)
			break
		}
	}
	require.True(t, found)
	logs, err := database.GetFullLog(task.ID)
	require.NoError(t, err)
	require.Contains(t, logs, "[imported 1 planned tasks]")
}

func TestRateLimitMarksTask(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "claude", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent(
			"You've hit your limit · resets 5pm (Europe/Vienna)",
			"KODAMA_RATELIMIT: Usage limit hit",
		), "claude"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))

	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusRateLimited
	}, 2*time.Second, 50*time.Millisecond)

	cp, err := database.GetLatestCheckpoint(task.ID)
	require.NoError(t, err)
	require.Nil(t, cp)
}

func TestRateLimitTextDoesNotMarkTask(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{QuestionTimeout: 2 * time.Second}
	d := New(cfg, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "claude", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newFakeAgent("This implementation should handle rate limits better."), "claude"
	})

	require.NoError(t, d.StartProject(context.Background(), proj.ID))

	require.Eventually(t, func() bool {
		got, _ := database.GetTask(task.ID)
		return got.Status == db.TaskStatusDone
	}, 2*time.Second, 50*time.Millisecond)
}

func TestExtractChecklistForRateLimit(t *testing.T) {
	out := strings.Join([]string{
		"- [x] Step 1",
		"- [ ] Step 2",
		"other",
	}, "\n")
	checklist := ExtractChecklist(out)
	require.Contains(t, checklist, "- [x] Step 1")
	require.Contains(t, checklist, "- [ ] Step 2")
}

type fakeTelegram struct {
	started bool
	notify  []string
}

func (f *fakeTelegram) Start(ctx context.Context)   { f.started = true }
func (f *fakeTelegram) SendNotification(msg string) { f.notify = append(f.notify, msg) }
func (f *fakeTelegram) SendQuestion(taskID int64, question string) (<-chan string, error) {
	ch := make(chan string, 1)
	return ch, nil
}

func TestUpdateTelegramSettingsHotReload(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()

	cfg := &config.Config{}
	d := New(cfg, database, nil, nil)

	var last *fakeTelegram
	d.telegramFactory = func(token string, userID int64) (telegramClient, error) {
		last = &fakeTelegram{}
		return last, nil
	}

	require.NoError(t, d.UpdateTelegramSettings("token", 123))
	require.NotNil(t, last)
	require.Eventually(t, func() bool {
		return last.started
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, d.notifier)

	require.NoError(t, d.UpdateTelegramSettings("", 0))
	require.Nil(t, d.notifier)
	require.Nil(t, d.qa)
}
