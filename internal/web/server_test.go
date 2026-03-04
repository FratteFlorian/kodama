package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })

	require.NoError(t, database.UpsertSettings("", 0))

	cfg := &config.Config{Port: 8080}
	hub := NewHub()
	envHub := NewHub()
	srv, err := New(cfg, database, hub, envHub, nil)
	require.NoError(t, err)
	return srv, database
}

func newTestServerNoSetup(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{Port: 8080}
	hub := NewHub()
	envHub := NewHub()
	srv, err := New(cfg, database, hub, envHub, nil)
	require.NoError(t, err)
	return srv, database
}

func TestIndexReturns200(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Kodama")
}

func TestAPIListProjectsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var projects []db.Project
	json.NewDecoder(rec.Body).Decode(&projects)
	assert.Empty(t, projects)
}

func TestAPICreateProject(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"name":"Test Project","repo_path":"/tmp/test","agent":"claude"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var proj db.Project
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&proj))
	assert.Equal(t, "Test Project", proj.Name)
	assert.Equal(t, "claude", proj.Agent)
}

func TestAPICreateAndListTasks(t *testing.T) {
	srv, database := newTestServer(t)

	// Create project first.
	proj, err := database.CreateProject("test", "/tmp", "", "claude", false)
	require.NoError(t, err)

	// Create task via API.
	body := `{"description":"implement auth","agent":"claude","profile":"qa","priority":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+itoa(proj.ID)+"/tasks",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// List tasks.
	req2 := httptest.NewRequest(http.MethodGet, "/api/projects/"+itoa(proj.ID)+"/tasks", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)

	var tasks []db.Task
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&tasks))
	assert.Len(t, tasks, 1)
	assert.Equal(t, "implement auth", tasks[0].Description)
	assert.Equal(t, "qa", tasks[0].Profile)
}

func TestAPIGetTask(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+itoa(task.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got db.Task
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	assert.Equal(t, task.ID, got.ID)
}

func TestAPIDeleteTask(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	task, _ := database.CreateTask(proj.ID, "do work", "", 0, false)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+itoa(task.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAPIUpdateTaskProfileCanClear(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	task, _ := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, database.UpdateTaskProfile(task.ID, "qa"))

	body := `{"profile":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+itoa(task.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	got, err := database.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "", got.Profile)
}

func TestCreateProjectHTMLRedirects(t *testing.T) {
	srv, _ := newTestServer(t)

	form := url.Values{}
	form.Set("name", "My Project")
	form.Set("repo_path", "")
	form.Set("agent", "claude")
	form.Set("prd", "")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/projects/")
}

func TestProjectPageReturns200(t *testing.T) {
	srv, database := newTestServer(t)

	proj, err := database.CreateProject("test", "/tmp", "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "test")
}

func TestCreateTaskWithAttachment(t *testing.T) {
	srv, database := newTestServer(t)
	proj, err := database.CreateProject("p", "/tmp", "", "claude", false)
	require.NoError(t, err)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("description", "task with file"))
	require.NoError(t, w.WriteField("agent", "codex"))
	require.NoError(t, w.WriteField("profile", "developer"))
	require.NoError(t, w.WriteField("priority", "0"))
	fw, err := w.CreateFormFile("attachments", "spec.pdf")
	require.NoError(t, err)
	_, err = fw.Write([]byte("pdf-content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/tasks", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	files, err := database.ListTaskAttachments(tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "spec.pdf", files[0].Name)
}

func TestCreateTaskRejectsUnsupportedAttachment(t *testing.T) {
	srv, database := newTestServer(t)
	proj, err := database.CreateProject("p", "/tmp", "", "claude", false)
	require.NoError(t, err)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("description", "task with bad file"))
	fw, err := w.CreateFormFile("attachments", "script.sh")
	require.NoError(t, err)
	_, err = fw.Write([]byte("#!/bin/sh"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/tasks", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	assert.Len(t, tasks, 0)
}

func TestDeleteAttachmentRemovesFileAndRecord(t *testing.T) {
	srv, database := newTestServer(t)
	proj, err := database.CreateProject("p", "/tmp", "", "claude", false)
	require.NoError(t, err)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "spec.pdf")
	require.NoError(t, os.WriteFile(path, []byte("pdf"), 0644))
	att, err := database.CreateProjectAttachment(proj.ID, "spec.pdf", path, "application/pdf", 3)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/attachments/"+itoa(att.ID)+"/delete", nil)
	req.Header.Set("Referer", "/projects/"+itoa(proj.ID))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/projects/"+itoa(proj.ID), rec.Header().Get("Location"))

	_, err = database.GetAttachment(att.ID)
	require.Error(t, err)
	_, err = os.Stat(path)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestTaskPageReturns200(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	task, err := database.CreateTask(proj.ID, "test task", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskProfile(task.ID, "ux-reviewer"))

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+itoa(task.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "test task")
	assert.Contains(t, rec.Body.String(), "ux-reviewer")
}

func TestStaticAssets(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSetupGateRedirectsWhenMissingSettings(t *testing.T) {
	srv, _ := newTestServerNoSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/setup", rec.Header().Get("Location"))
}

func TestSetupSaveEnablesApp(t *testing.T) {
	srv, database := newTestServerNoSetup(t)

	form := url.Values{}
	form.Set("telegram_token", "")
	form.Set("telegram_user_id", "")
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))

	settings, err := database.GetSettings()
	require.NoError(t, err)
	require.NotNil(t, settings)
}

type fakeDaemon struct {
	token     string
	userID    int64
	answerErr error
}

func (f *fakeDaemon) StartProject(ctx context.Context, projectID int64) error { return nil }
func (f *fakeDaemon) StopProject(projectID int64)                             {}
func (f *fakeDaemon) IsRunning(projectID int64) bool                          { return false }
func (f *fakeDaemon) AnswerQuestion(taskID int64, answer string) error {
	return f.answerErr
}
func (f *fakeDaemon) StartEnvironment(ctx context.Context, projectID int64) error {
	return nil
}
func (f *fakeDaemon) StopEnvironment(projectID int64) error { return nil }
func (f *fakeDaemon) RestartEnvironment(ctx context.Context, projectID int64) error {
	return nil
}
func (f *fakeDaemon) IsEnvRunning(projectID int64) bool { return false }
func (f *fakeDaemon) UpdateTelegramSettings(token string, userID int64) error {
	f.token = token
	f.userID = userID
	return nil
}

func TestSettingsSaveCallsDaemon(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })
	require.NoError(t, database.UpsertSettings("", 0))

	cfg := &config.Config{Port: 8080}
	hub := NewHub()
	envHub := NewHub()
	fd := &fakeDaemon{}
	srv, err := New(cfg, database, hub, envHub, fd)
	require.NoError(t, err)

	form := url.Values{}
	form.Set("telegram_token", "abc")
	form.Set("telegram_user_id", "42")
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "abc", fd.token)
	assert.Equal(t, int64(42), fd.userID)
}

func TestAnswerQuestionRendersTaskWithError(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { database.Close() })
	require.NoError(t, database.UpsertSettings("", 0))

	cfg := &config.Config{Port: 8080}
	hub := NewHub()
	envHub := NewHub()
	fd := &fakeDaemon{answerErr: fmt.Errorf("no waiting question for task 1")}
	srv, err := New(cfg, database, hub, envHub, fd)
	require.NoError(t, err)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)
	require.NoError(t, database.UpdateTaskStatus(task.ID, db.TaskStatusWaiting))

	form := url.Values{}
	form.Set("answer", "Use sqlite")
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+itoa(task.ID)+"/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "Agent is waiting for input")
	assert.Contains(t, rec.Body.String(), "no waiting question for task 1")
}

func TestEnvironmentConfigureAndPage(t *testing.T) {
	srv, database := newTestServer(t)

	proj, err := database.CreateProject("proj", "/tmp", "", "claude", false)
	require.NoError(t, err)

	// Configure environment via POST.
	form := url.Values{}
	form.Set("type", "compose")
	form.Set("config_path", "docker-compose.yml")
	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/environment",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)

	// Verify env was saved.
	env, err := database.GetEnvironment(proj.ID)
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.Equal(t, "compose", env.Type)
	assert.Equal(t, "docker-compose.yml", env.ConfigPath)

	// GET environment log page.
	req2 := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/environment", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "Dev Environment")
	assert.Contains(t, rec2.Body.String(), "docker-compose.yml")
}

func TestEnvironmentPageRedirectsWhenNotConfigured(t *testing.T) {
	srv, database := newTestServer(t)

	proj, err := database.CreateProject("proj", "/tmp", "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/environment", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	// No env configured → redirect to project page.
	assert.Equal(t, http.StatusSeeOther, rec.Code)
}

func TestRetryTaskCreatesNewTask(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	failed, _ := database.CreateTask(proj.ID, "do work", "codex", 0, true)
	database.UpdateTaskProfile(failed.ID, "incident")
	database.UpdateTaskStatus(failed.ID, db.TaskStatusFailed)

	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/tasks/"+itoa(failed.ID)+"/retry", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "do work", tasks[1].Description)
	assert.Equal(t, "codex", tasks[1].Agent)
	assert.Equal(t, "incident", tasks[1].Profile)
	assert.True(t, tasks[1].Failover)
}

func TestCreateWorkflowTasks(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)

	form := url.Values{}
	form.Set("goal", "Improve onboarding UX")
	form.Set("agent", "codex")
	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/tasks/workflow", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	assert.Equal(t, "architect", tasks[0].Profile)
	assert.Equal(t, "developer", tasks[1].Profile)
	assert.Equal(t, "qa", tasks[2].Profile)
	assert.Equal(t, "codex", tasks[0].Agent)
	assert.Contains(t, tasks[0].Description, "Improve onboarding UX")
	assert.Contains(t, tasks[2].Description, "Improve onboarding UX")
}

func TestPlanTasksFromPRDCreatesPlannerTask(t *testing.T) {
	srv, database := newTestServer(t)
	proj, _ := database.CreateProject("p", "/tmp", "", "codex", false)

	form := url.Values{}
	form.Set("agent", "claude")
	req := httptest.NewRequest(http.MethodPost, "/projects/"+itoa(proj.ID)+"/tasks/plan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusSeeOther, rec.Code)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "architect", tasks[0].Profile)
	assert.Equal(t, "claude", tasks[0].Agent)
	assert.Contains(t, tasks[0].Description, "KODAMA_TASKS_BEGIN")
}

func TestProjectPageShowsEnvironmentPanel(t *testing.T) {
	srv, database := newTestServer(t)

	proj, err := database.CreateProject("proj", "/tmp", "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Command Runtime")
}

func TestProjectFilesDirectoryListing(t *testing.T) {
	srv, database := newTestServer(t)
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# hi\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "src"), 0755))

	proj, err := database.CreateProject("proj", repo, "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/files", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "README.md")
	assert.Contains(t, rec.Body.String(), "src")
}

func TestProjectFilePreviewAndRaw(t *testing.T) {
	srv, database := newTestServer(t)
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("hello file browser"), 0644))

	proj, err := database.CreateProject("proj", repo, "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/files?path=notes.txt", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "hello file browser")

	req2 := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/files/raw?path=notes.txt", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "hello file browser")
}

func TestProjectFilesRejectsTraversal(t *testing.T) {
	srv, database := newTestServer(t)
	repo := t.TempDir()
	proj, err := database.CreateProject("proj", repo, "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID)+"/files?path=../etc/passwd", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
