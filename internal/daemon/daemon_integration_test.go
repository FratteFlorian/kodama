package daemon

import (
	"context"
	"strings"
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
		return newFakeAgent("You've hit your limit · resets 5pm (Europe/Vienna)"), "claude"
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
