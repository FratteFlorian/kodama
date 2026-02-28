package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	body := `{"description":"implement auth","agent":"claude","priority":0}`
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

func TestTaskPageReturns200(t *testing.T) {
	srv, database := newTestServer(t)

	proj, _ := database.CreateProject("p", "/tmp", "", "claude", false)
	task, err := database.CreateTask(proj.ID, "test task", "", 0, false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+itoa(task.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "test task")
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
	token  string
	userID int64
}

func (f *fakeDaemon) StartProject(ctx context.Context, projectID int64) error { return nil }
func (f *fakeDaemon) StopProject(projectID int64)                             {}
func (f *fakeDaemon) IsRunning(projectID int64) bool                          { return false }
func (f *fakeDaemon) AnswerQuestion(taskID int64, answer string) error        { return nil }
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
	assert.True(t, tasks[1].Failover)
}

func TestProjectPageShowsEnvironmentPanel(t *testing.T) {
	srv, database := newTestServer(t)

	proj, err := database.CreateProject("proj", "/tmp", "", "claude", false)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+itoa(proj.ID), nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	// Environment panel should always be present.
	assert.Contains(t, rec.Body.String(), "Dev Environment")
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
