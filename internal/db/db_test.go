package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestProjectCRUD(t *testing.T) {
	db := openTestDB(t)

	// Create
	p, err := db.CreateProject("test-project", "/repo", "golang:1.22", "claude", false)
	require.NoError(t, err)
	assert.Equal(t, "test-project", p.Name)
	assert.Equal(t, "/repo", p.RepoPath)
	assert.Equal(t, "golang:1.22", p.DockerImage)
	assert.Equal(t, "claude", p.Agent)
	assert.False(t, p.Failover)

	// Get
	got, err := db.GetProject(p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.Name, got.Name)

	// List
	list, err := db.ListProjects()
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// Update
	err = db.UpdateProject(p.ID, "updated", "/new", "node:20", "codex", true)
	require.NoError(t, err)
	got, err = db.GetProject(p.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Name)
	assert.True(t, got.Failover)

	// Delete
	err = db.DeleteProject(p.ID)
	require.NoError(t, err)
	list, err = db.ListProjects()
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestTaskCRUD(t *testing.T) {
	db := openTestDB(t)

	p, err := db.CreateProject("proj", "/repo", "", "claude", false)
	require.NoError(t, err)

	// Create
	task, err := db.CreateTask(p.ID, "implement feature X", "", 0, false)
	require.NoError(t, err)
	assert.Equal(t, TaskStatusPending, task.Status)
	assert.Equal(t, "implement feature X", task.Description)

	// Get
	got, err := db.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, task.ID, got.ID)

	// List
	list, err := db.ListTasks(p.ID)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// Update status
	err = db.UpdateTaskStatus(task.ID, TaskStatusRunning)
	require.NoError(t, err)
	got, err = db.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskStatusRunning, got.Status)
	assert.NotNil(t, got.StartedAt)

	err = db.UpdateTaskStatus(task.ID, TaskStatusDone)
	require.NoError(t, err)
	got, err = db.GetTask(task.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.CompletedAt)

	// Delete
	err = db.DeleteTask(task.ID)
	require.NoError(t, err)
	list, err = db.ListTasks(p.ID)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestTaskLogs(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	task, _ := db.CreateTask(p.ID, "task", "", 0, false)

	err := db.AppendTaskLog(task.ID, "line 1\n")
	require.NoError(t, err)
	err = db.AppendTaskLog(task.ID, "line 2\n")
	require.NoError(t, err)

	full, err := db.GetFullLog(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "line 1\nline 2\n", full)

	logs, err := db.GetTaskLogs(task.ID)
	require.NoError(t, err)
	assert.Len(t, logs, 2)
}

func TestCheckpoints(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	task, _ := db.CreateTask(p.ID, "task", "", 0, false)

	// No checkpoint yet
	cp, err := db.GetLatestCheckpoint(task.ID)
	require.NoError(t, err)
	assert.Nil(t, cp)

	// Save checkpoint
	err = db.SaveCheckpoint(task.ID, "- [x] Step 1\n- [ ] Step 2")
	require.NoError(t, err)

	cp, err = db.GetLatestCheckpoint(task.ID)
	require.NoError(t, err)
	require.NotNil(t, cp)
	assert.Equal(t, "- [x] Step 1\n- [ ] Step 2", cp.ChecklistState)

	// Latest is returned
	err = db.SaveCheckpoint(task.ID, "- [x] Step 1\n- [x] Step 2")
	require.NoError(t, err)
	cp, err = db.GetLatestCheckpoint(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "- [x] Step 1\n- [x] Step 2", cp.ChecklistState)
}

func TestListPendingTasks(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	t1, _ := db.CreateTask(p.ID, "task1", "", 0, false)
	t2, _ := db.CreateTask(p.ID, "task2", "", 1, false)
	t3, _ := db.CreateTask(p.ID, "task3", "", 2, false)

	db.UpdateTaskStatus(t2.ID, TaskStatusDone)
	db.UpdateTaskStatus(t3.ID, TaskStatusRateLimited)

	pending, err := db.ListPendingTasks(p.ID)
	require.NoError(t, err)
	// only t1 is pending — t2 is done, t3 is rate_limited (not ready)
	assert.Len(t, pending, 1)
	ids := []int64{pending[0].ID}
	assert.Contains(t, ids, t1.ID)
}

func TestListPendingTasksIncludesReadyRateLimited(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	t1, _ := db.CreateTask(p.ID, "task1", "", 0, false)
	t2, _ := db.CreateTask(p.ID, "task2", "", 1, false)

	db.UpdateTaskStatus(t2.ID, TaskStatusRateLimited)
	require.NoError(t, db.UpdateTaskRetryAfter(t2.ID, time.Now().Add(-1*time.Minute)))

	pending, err := db.ListPendingTasks(p.ID)
	require.NoError(t, err)
	assert.Len(t, pending, 2)
	ids := []int64{pending[0].ID, pending[1].ID}
	assert.Contains(t, ids, t1.ID)
	assert.Contains(t, ids, t2.ID)
}

func TestCascadeDelete(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	task, _ := db.CreateTask(p.ID, "task", "", 0, false)
	db.AppendTaskLog(task.ID, "output")
	db.SaveCheckpoint(task.ID, "state")

	err := db.DeleteProject(p.ID)
	require.NoError(t, err)

	// Tasks should be gone (cascade)
	tasks, err := db.ListTasks(p.ID)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestEnvironmentCRUD(t *testing.T) {
	db := openTestDB(t)

	p, err := db.CreateProject("proj", "/repo", "", "claude", false)
	require.NoError(t, err)

	// No env yet — should return nil, nil.
	env, err := db.GetEnvironment(p.ID)
	require.NoError(t, err)
	assert.Nil(t, env)

	// Upsert (create).
	env, err = db.UpsertEnvironment(p.ID, "compose", "docker-compose.yml")
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Equal(t, "compose", env.Type)
	assert.Equal(t, "docker-compose.yml", env.ConfigPath)
	assert.Equal(t, EnvironmentStatusStopped, env.Status)

	// Upsert (update).
	env, err = db.UpsertEnvironment(p.ID, "dockerfile", "Dockerfile")
	require.NoError(t, err)
	assert.Equal(t, "dockerfile", env.Type)
	assert.Equal(t, "Dockerfile", env.ConfigPath)

	// Update status.
	err = db.UpdateEnvironmentStatus(env.ID, EnvironmentStatusRunning)
	require.NoError(t, err)
	env, _ = db.GetEnvironment(p.ID)
	assert.Equal(t, EnvironmentStatusRunning, env.Status)
	assert.NotNil(t, env.StartedAt)

	err = db.UpdateEnvironmentStatus(env.ID, EnvironmentStatusStopped)
	require.NoError(t, err)
	env, _ = db.GetEnvironment(p.ID)
	assert.Equal(t, EnvironmentStatusStopped, env.Status)
	assert.NotNil(t, env.StoppedAt)
}

func TestEnvironmentLogs(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	env, err := db.UpsertEnvironment(p.ID, "compose", "docker-compose.yml")
	require.NoError(t, err)

	err = db.AppendEnvironmentLog(env.ID, "starting...\n")
	require.NoError(t, err)
	err = db.AppendEnvironmentLog(env.ID, "ready\n")
	require.NoError(t, err)

	log, err := db.GetEnvironmentLog(env.ID)
	require.NoError(t, err)
	assert.Equal(t, "starting...\nready\n", log)
}

func TestEnvironmentCascadeDeleteWithProject(t *testing.T) {
	db := openTestDB(t)

	p, _ := db.CreateProject("proj", "/repo", "", "claude", false)
	env, err := db.UpsertEnvironment(p.ID, "compose", "docker-compose.yml")
	require.NoError(t, err)
	db.AppendEnvironmentLog(env.ID, "log line\n")

	// Delete project → env and logs should cascade.
	err = db.DeleteProject(p.ID)
	require.NoError(t, err)

	// Env should be gone.
	got, err := db.GetEnvironment(p.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestAttachmentsCRUDAndClone(t *testing.T) {
	db := openTestDB(t)
	p, err := db.CreateProject("proj", "/repo", "", "claude", false)
	require.NoError(t, err)
	t1, err := db.CreateTask(p.ID, "task", "", 0, false)
	require.NoError(t, err)
	t2, err := db.CreateTask(p.ID, "task2", "", 1, false)
	require.NoError(t, err)

	tmp := t.TempDir()
	pf := filepath.Join(tmp, "spec.pdf")
	require.NoError(t, os.WriteFile(pf, []byte("pdf"), 0644))
	tf := filepath.Join(tmp, "shot.png")
	require.NoError(t, os.WriteFile(tf, []byte("img"), 0644))

	pa, err := db.CreateProjectAttachment(p.ID, "spec.pdf", pf, "application/pdf", 3)
	require.NoError(t, err)
	require.NotNil(t, pa.ProjectID)
	require.Nil(t, pa.TaskID)

	ta, err := db.CreateTaskAttachment(t1.ID, "shot.png", tf, "image/png", 3)
	require.NoError(t, err)
	require.NotNil(t, ta.TaskID)

	projectFiles, err := db.ListProjectAttachments(p.ID)
	require.NoError(t, err)
	require.Len(t, projectFiles, 1)

	taskFiles, err := db.ListTaskAttachments(t1.ID)
	require.NoError(t, err)
	require.Len(t, taskFiles, 1)

	require.NoError(t, db.CloneTaskAttachments(t1.ID, t2.ID))
	cloned, err := db.ListTaskAttachments(t2.ID)
	require.NoError(t, err)
	require.Len(t, cloned, 1)
	assert.Equal(t, "shot.png", cloned[0].Name)
}

func TestSettingsUpsertAndGet(t *testing.T) {
	db := openTestDB(t)

	settings, err := db.GetSettings()
	require.NoError(t, err)
	assert.Nil(t, settings)

	require.NoError(t, db.UpsertSettings("token", 1234))
	settings, err = db.GetSettings()
	require.NoError(t, err)
	require.NotNil(t, settings)
	assert.Equal(t, "token", settings.TelegramToken)
	assert.Equal(t, int64(1234), settings.TelegramUserID)
}
