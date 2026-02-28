package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/florian/kodama/internal/daemon"
	"github.com/florian/kodama/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     sameOrigin,
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	originHost := strings.Split(u.Host, ":")[0]
	reqHost := strings.Split(r.Host, ":")[0]
	if originHost == reqHost {
		return true
	}
	// Allow localhost variants.
	if (originHost == "localhost" || originHost == "127.0.0.1") &&
		(reqHost == "localhost" || reqHost == "127.0.0.1") {
		return true
	}
	return false
}

// --- HTML handlers ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	projects, err := s.db.ListProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderTemplate(w, "index.html", map[string]any{
		"Projects": projects,
	})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	repoPath := expandPath(r.FormValue("repo_path"))
	agent := r.FormValue("agent")
	prd := r.FormValue("prd")
	if agent == "" {
		agent = "codex"
	}

	slog.Info("creating project", "name", name, "repo_path", repoPath, "agent", agent)

	proj, err := s.db.CreateProject(name, repoPath, "", agent, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("project created", "project_id", proj.ID, "name", name, "repo_path", repoPath, "agent", agent)

	// Initialize project files if repo path is set.
	if repoPath != "" {
		slog.Info("initializing project files", "repo_path", repoPath)
		if err := daemon.InitProject(repoPath, name, prd, "", agent); err != nil {
			slog.Warn("init project files failed", "repo_path", repoPath, "err", err)
		} else {
			slog.Info("project files written", "repo_path", repoPath)
		}
	} else {
		slog.Info("no repo_path set — skipping project file init")
	}

	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	tasks, err := s.db.ListTasks(proj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var totalCost float64
	for _, t := range tasks {
		totalCost += t.CostUSD
	}
	env, _ := s.db.GetEnvironment(proj.ID) // nil if not configured yet
	s.renderTemplate(w, "project.html", map[string]any{
		"Project":      proj,
		"Tasks":        tasks,
		"IsRunning":    s.daemon != nil && s.daemon.IsRunning(proj.ID),
		"Env":          env,
		"IsEnvRunning": s.daemon != nil && s.daemon.IsEnvRunning(proj.ID),
		"TotalCost":    totalCost,
		"Msg":          r.URL.Query().Get("msg"),
	})
}

func (s *Server) handleUpdateProjectSettings(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dockerImage := r.FormValue("docker_image")
	agent := r.FormValue("agent")
	if agent == "" {
		agent = proj.Agent
	}
	if err := s.db.UpdateProject(proj.ID, proj.Name, proj.RepoPath, dockerImage, agent, proj.Failover); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("project settings updated", "project_id", proj.ID, "docker_image", dockerImage, "agent", agent)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	desc := r.FormValue("description")
	agent := r.FormValue("agent")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	failover := r.FormValue("failover") == "on"

	task, err := s.db.CreateTask(proj.ID, desc, agent, priority, failover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("task created", "task_id", task.ID, "project_id", proj.ID, "agent", agent, "priority", priority)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if agent := r.FormValue("agent"); agent != "" {
		s.db.UpdateTaskAgent(tid, agent)
	}
	if pStr := r.FormValue("priority"); pStr != "" {
		p, _ := strconv.Atoi(pStr)
		s.db.UpdateTaskPriority(tid, p)
	}
	proj, _ := s.getProject(r)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

// handleUpdateTaskAgent is a POST-only route for HTML form agent changes.
func (s *Server) handleUpdateTaskAgent(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.db.UpdateTaskAgent(tid, r.FormValue("agent"))
	proj, _ := s.getProject(r)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

// handleUpdateTaskFailover is a POST-only route for HTML form failover changes.
func (s *Server) handleUpdateTaskFailover(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	failover := r.FormValue("failover") == "on"
	s.db.UpdateTaskFailover(tid, failover)
	proj, _ := s.getProject(r)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.db.DeleteTask(tid)
	proj, _ := s.getProject(r)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	task, err := s.db.GetTask(tid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	nextPriority, err := s.db.NextTaskPriority(task.ProjectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = s.db.CreateTask(task.ProjectID, task.Description, task.Agent, nextPriority, task.Failover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj, _ := s.getProject(r)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleStartProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	pending, err := s.db.ListPendingTasks(proj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(pending) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/projects/%d?msg=no_tasks", proj.ID), http.StatusSeeOther)
		return
	}
	if s.daemon != nil {
		slog.Info("starting project via web", "project_id", proj.ID, "pending_tasks", len(pending))
		if err := s.daemon.StartProject(context.Background(), proj.ID); err != nil {
			slog.Warn("start project failed", "project_id", proj.ID, "err", err)
			http.Redirect(w, r, fmt.Sprintf("/projects/%d?msg=already_running", proj.ID), http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d?msg=started", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleStopProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.daemon != nil {
		s.daemon.StopProject(proj.ID)
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	task, err := s.db.GetTask(taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	logContent, _ := s.db.GetFullLog(taskID)
	proj, _ := s.db.GetProject(task.ProjectID)
	s.renderTemplate(w, "task.html", map[string]any{
		"Task":    task,
		"Project": proj,
		"Log":     logContent,
	})
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer := r.FormValue("answer")
	if s.daemon != nil {
		s.daemon.AnswerQuestion(taskID, answer)
	}
	http.Redirect(w, r, fmt.Sprintf("/tasks/%d", taskID), http.StatusSeeOther)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	slog.Debug("websocket connected", "task_id", taskID, "remote", r.RemoteAddr)

	// Send existing log as initial content.
	logContent, _ := s.db.GetFullLog(taskID)
	if logContent != "" {
		conn.WriteMessage(websocket.TextMessage, []byte(logContent))
	}

	s.hub.Register(taskID, conn)
}

// --- Environment handlers ---

func (s *Server) handleEnvironmentPage(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	env, err := s.db.GetEnvironment(proj.ID)
	if err != nil || env == nil {
		http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
		return
	}
	s.renderTemplate(w, "environment.html", map[string]any{
		"Project":   proj,
		"Env":       env,
		"IsRunning": s.daemon != nil && s.daemon.IsEnvRunning(proj.ID),
	})
}

func (s *Server) handleEnvironmentConfigure(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	envType := r.FormValue("type")
	if envType == "" {
		envType = "compose"
	}
	configPath := r.FormValue("config_path")
	if _, err := s.db.UpsertEnvironment(proj.ID, envType, configPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("environment configured", "project_id", proj.ID, "type", envType, "config_path", configPath)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleEnvironmentStart(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.StartEnvironment(context.Background(), proj.ID); err != nil {
			slog.Warn("start environment failed", "project_id", proj.ID, "err", err)
		}
	}
	// Redirect to environment log page so the user can watch the startup.
	env, _ := s.db.GetEnvironment(proj.ID)
	if env != nil {
		http.Redirect(w, r, fmt.Sprintf("/projects/%d/environment", proj.ID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleEnvironmentStop(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.StopEnvironment(proj.ID); err != nil {
			slog.Warn("stop environment failed", "project_id", proj.ID, "err", err)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d/environment", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleEnvironmentRestart(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.RestartEnvironment(context.Background(), proj.ID); err != nil {
			slog.Warn("restart environment failed", "project_id", proj.ID, "err", err)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d/environment", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleEnvironmentWebSocket(w http.ResponseWriter, r *http.Request) {
	envID, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	slog.Debug("env websocket connected", "env_id", envID, "remote", r.RemoteAddr)

	// Send existing log as initial content.
	logContent, _ := s.db.GetEnvironmentLog(envID)
	if logContent != "" {
		conn.WriteMessage(websocket.TextMessage, []byte(logContent))
	}

	s.envHub.Register(envID, conn)
}

// --- JSON API handlers ---

func (s *Server) apiListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.db.ListProjects()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, projects)
}

func (s *Server) apiGetProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonOK(w, proj)
}

func (s *Server) apiCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		RepoPath    string `json:"repo_path"`
		DockerImage string `json:"docker_image"`
		Agent       string `json:"agent"`
		PRD         string `json:"prd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Agent == "" {
		req.Agent = "codex"
	}
	proj, err := s.db.CreateProject(req.Name, req.RepoPath, req.DockerImage, req.Agent, false)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, proj)
}

func (s *Server) apiListTasks(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	tasks, err := s.db.ListTasks(proj.ID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, tasks)
}

func (s *Server) apiCreateTask(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	var req struct {
		Description string `json:"description"`
		Agent       string `json:"agent"`
		Priority    int    `json:"priority"`
		Failover    bool   `json:"failover"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	task, err := s.db.CreateTask(proj.ID, req.Description, req.Agent, req.Priority, req.Failover)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, task)
}

func (s *Server) apiGetTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	task, err := s.db.GetTask(taskID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonOK(w, task)
}

func (s *Server) apiUpdateTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Agent    string `json:"agent"`
		Priority *int   `json:"priority"`
		Failover *bool  `json:"failover"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Agent != "" {
		s.db.UpdateTaskAgent(taskID, req.Agent)
	}
	if req.Priority != nil {
		s.db.UpdateTaskPriority(taskID, *req.Priority)
	}
	if req.Failover != nil {
		s.db.UpdateTaskFailover(taskID, *req.Failover)
	}
	task, _ := s.db.GetTask(taskID)
	jsonOK(w, task)
}

func (s *Server) apiDeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.db.DeleteTask(taskID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiStartProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.StartProject(context.Background(), proj.ID); err != nil {
			jsonError(w, err.Error(), http.StatusConflict)
			return
		}
	}
	jsonOK(w, map[string]string{"status": "started"})
}

func (s *Server) apiAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	taskID, err := getIDParam(r, "id")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.AnswerQuestion(taskID, req.Answer); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	jsonOK(w, map[string]string{"status": "answered"})
}

// expandPath expands ~ to the home directory in a file path.
func expandPath(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// --- Helpers ---

func (s *Server) getProject(r *http.Request) (*db.Project, error) {
	id, err := getIDParam(r, "id")
	if err != nil {
		return nil, err
	}
	return s.db.GetProject(id)
}

func getIDParam(r *http.Request, param string) (int64, error) {
	s := chi.URLParam(r, param)
	if s == "" {
		return 0, fmt.Errorf("missing %s parameter", param)
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", param, err)
	}
	return id, nil
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
