package web

import (
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

	cfg := &config.Config{Port: 8080}
	hub := NewHub()
	srv, err := New(cfg, database, hub, nil)
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
	task, err := database.CreateTask(proj.ID, "do work", "", 0)
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
	task, _ := database.CreateTask(proj.ID, "do work", "", 0)

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
	task, err := database.CreateTask(proj.ID, "test task", "", 0)
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

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
