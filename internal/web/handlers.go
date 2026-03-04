package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/florian/kodama/internal/daemon"
	"github.com/florian/kodama/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const maxPreviewBytes = 512 * 1024

type fileBrowserEntry struct {
	Name      string
	RelPath   string
	IsDir     bool
	SizeBytes int64
	ModTime   string
	LinkPath  string
	Download  string
}

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

func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isSetupComplete() ||
			strings.HasPrefix(r.URL.Path, "/setup") ||
			strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	})
}

func (s *Server) isSetupComplete() bool {
	settings, err := s.db.GetSettings()
	if err != nil {
		return false
	}
	return settings != nil
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
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		if !errors.Is(err, http.ErrNotMultipart) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	name := r.FormValue("name")
	repoPath := expandPath(r.FormValue("repo_path"))
	agent := r.FormValue("agent")
	runtimeMode := normalizeRuntimeMode(r.FormValue("runtime_mode"))
	prd := r.FormValue("prd")
	if r.MultipartForm != nil {
		if err := validateAttachments(r.MultipartForm.File["attachments"]); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if agent == "" {
		agent = "codex"
	}
	if runtimeMode == "" {
		runtimeMode = "docker"
	}

	slog.Info("creating project", "name", name, "repo_path", repoPath, "agent", agent)

	proj, err := s.db.CreateProject(name, repoPath, "", agent, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateProjectRuntimeMode(proj.ID, runtimeMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj.RuntimeMode = runtimeMode
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
	if r.MultipartForm != nil {
		if err := s.saveAttachments("attachments", proj.ID, 0, r.MultipartForm.File["attachments"]); err != nil {
			if isAttachmentValidationError(err) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
	projectAttachments, _ := s.db.ListProjectAttachments(proj.ID)
	s.renderTemplate(w, "project.html", map[string]any{
		"Project":      proj,
		"Tasks":        tasks,
		"ProjectFiles": projectAttachments,
		"IsRunning":    s.daemon != nil && s.daemon.IsRunning(proj.ID),
		"Env":          env,
		"IsEnvRunning": s.daemon != nil && s.daemon.IsEnvRunning(proj.ID),
		"TotalCost":    totalCost,
		"Msg":          r.URL.Query().Get("msg"),
	})
}

func (s *Server) handleProjectFiles(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if strings.TrimSpace(proj.RepoPath) == "" {
		http.Error(w, "project has no repository path", http.StatusBadRequest)
		return
	}
	root, rel, target, err := resolveProjectPath(proj.RepoPath, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "path not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Project": proj,
		"RelPath": rel,
		"IsDir":   info.IsDir(),
	}

	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := make([]fileBrowserEntry, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			inf, err := e.Info()
			if err != nil {
				continue
			}
			nextRel := filepath.ToSlash(filepath.Join(rel, name))
			items = append(items, fileBrowserEntry{
				Name:      name,
				RelPath:   nextRel,
				IsDir:     e.IsDir(),
				SizeBytes: inf.Size(),
				ModTime:   inf.ModTime().Format("2006-01-02 15:04"),
				LinkPath:  fmt.Sprintf("/projects/%d/files?path=%s", proj.ID, url.QueryEscape(nextRel)),
				Download:  fmt.Sprintf("/projects/%d/files/raw?path=%s", proj.ID, url.QueryEscape(nextRel)),
			})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir != items[j].IsDir {
				return items[i].IsDir
			}
			return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		})
		data["Entries"] = items
		if rel != "" {
			parent := filepath.ToSlash(filepath.Dir(rel))
			if parent == "." {
				parent = ""
			}
			data["ParentLink"] = fmt.Sprintf("/projects/%d/files?path=%s", proj.ID, url.QueryEscape(parent))
		}
	} else {
		f, err := os.Open(target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		buf := make([]byte, maxPreviewBytes+1)
		n, _ := io.ReadFull(f, buf)
		if n < 0 {
			n = 0
		}
		content := buf[:minInt(n, maxPreviewBytes)]
		truncated := n > maxPreviewBytes

		isText := utf8.Valid(content)
		if isText {
			data["FileContent"] = string(content)
			data["Truncated"] = truncated
		} else {
			data["Binary"] = true
		}
		data["DownloadLink"] = fmt.Sprintf("/projects/%d/files/raw?path=%s", proj.ID, url.QueryEscape(rel))
		data["FileSize"] = info.Size()
		data["Root"] = root
	}

	s.renderTemplate(w, "files.html", data)
}

func (s *Server) handleProjectFileRaw(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if strings.TrimSpace(proj.RepoPath) == "" {
		http.Error(w, "project has no repository path", http.StatusBadRequest)
		return
	}
	_, rel, target, err := resolveProjectPath(proj.RepoPath, r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "path not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot download a directory", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(rel)))
	http.ServeFile(w, r, target)
}

func resolveProjectPath(repoPath, rawRel string) (root string, rel string, target string, err error) {
	root, err = filepath.Abs(repoPath)
	if err != nil {
		return "", "", "", err
	}
	rel = strings.TrimSpace(rawRel)
	rel = strings.TrimPrefix(rel, "/")
	clean := filepath.Clean(rel)
	if clean == "." {
		clean = ""
	}
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", "", "", fmt.Errorf("invalid path")
	}
	target = filepath.Join(root, clean)
	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", "", "", fmt.Errorf("path escape")
	}
	return root, filepath.ToSlash(clean), target, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	runtimeMode := normalizeRuntimeMode(r.FormValue("runtime_mode"))
	agent := r.FormValue("agent")
	if agent == "" {
		agent = proj.Agent
	}
	if err := s.db.UpdateProject(proj.ID, proj.Name, proj.RepoPath, proj.DockerImage, agent, proj.Failover); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateProjectRuntimeMode(proj.ID, runtimeMode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("project settings updated", "project_id", proj.ID, "runtime_mode", runtimeMode, "agent", agent)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		if !errors.Is(err, http.ErrNotMultipart) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	desc := r.FormValue("description")
	agent := r.FormValue("agent")
	profile := normalizeTaskProfile(r.FormValue("profile"))
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	failover := r.FormValue("failover") == "on"
	if r.MultipartForm != nil {
		if err := validateAttachments(r.MultipartForm.File["attachments"]); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	task, err := s.db.CreateTask(proj.ID, desc, agent, priority, failover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateTaskProfile(task.ID, profile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.MultipartForm != nil {
		if err := s.saveAttachments("attachments", proj.ID, task.ID, r.MultipartForm.File["attachments"]); err != nil {
			if isAttachmentValidationError(err) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	slog.Info("task created", "task_id", task.ID, "project_id", proj.ID, "agent", agent, "priority", priority)
	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handlePlanTasksFromPRD(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	agent := strings.TrimSpace(r.FormValue("agent"))
	priority, err := s.db.NextTaskPriority(proj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	planPrompt := `Read the full PRD/project context (including attached files) and create a concrete implementation backlog.

Output ONLY one structured task plan block using this exact format:

KODAMA_TASKS_BEGIN
[
  {
    "title": "short task title",
    "description": "clear implementation-oriented task description",
    "priority": 0,
    "profile": "developer",
    "agent": "",
    "failover": false
  }
]
KODAMA_TASKS_END

Rules:
- Return valid JSON array between the markers.
- Include 5-20 tasks, ordered by execution sequence.
- Use profiles from: architect, developer, qa, refactorer, incident, ux-reviewer.
- Use agent values: "", "claude", or "codex".
- Avoid duplicates and vague tasks.
- Do not include any text outside the marker block.`
	if proj.RuntimeMode == "docker" {
		planPrompt += `
- This project uses Docker runtime for commands. Docker setup is mandatory.
- Ensure backlog includes creating/updating a production-quality Dockerfile and docker-compose.yml based on the actual stack.
- Include explicit containerized validation tasks (build/test/lint commands executed in Docker).`
	}

	task, err := s.db.CreateTask(proj.ID, planPrompt, agent, priority, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateTaskProfile(task.ID, "architect"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/projects/%d", proj.ID), http.StatusSeeOther)
}

func (s *Server) handleCreateWorkflowTasks(w http.ResponseWriter, r *http.Request) {
	proj, err := s.getProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	goal := strings.TrimSpace(r.FormValue("goal"))
	if goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	agent := strings.TrimSpace(r.FormValue("agent"))

	nextPriority, err := s.db.NextTaskPriority(proj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	steps := []struct {
		profile string
		desc    string
	}{
		{
			profile: "architect",
			desc:    fmt.Sprintf("Architecture plan for: %s. Define approach, trade-offs, interfaces, migration notes, and implementation plan.", goal),
		},
		{
			profile: "developer",
			desc:    fmt.Sprintf("Implement based on approved architecture for: %s. Keep scope focused and update/add tests as needed.", goal),
		},
		{
			profile: "qa",
			desc:    fmt.Sprintf("QA review for: %s. Validate behavior, edge cases, regressions, and test coverage. Report findings by severity.", goal),
		},
	}

	for i, step := range steps {
		t, err := s.db.CreateTask(proj.ID, step.desc, agent, nextPriority+i, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.UpdateTaskProfile(t.ID, step.profile); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

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
	if _, ok := r.Form["profile"]; ok {
		s.db.UpdateTaskProfile(tid, normalizeTaskProfile(r.FormValue("profile")))
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

// handleUpdateTaskProfile is a POST-only route for HTML form profile changes.
func (s *Server) handleUpdateTaskProfile(w http.ResponseWriter, r *http.Request) {
	tid, err := getIDParam(r, "tid")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.db.UpdateTaskProfile(tid, normalizeTaskProfile(r.FormValue("profile")))
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
	retryTask, err := s.db.CreateTask(task.ProjectID, task.Description, task.Agent, nextPriority, task.Failover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.UpdateTaskProfile(retryTask.ID, task.Profile)
	s.db.CloneTaskAttachments(task.ID, retryTask.ID)
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
	s.renderTaskPage(w, taskID, "", http.StatusOK)
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
		if err := s.daemon.AnswerQuestion(taskID, answer); err != nil {
			s.renderTaskPage(w, taskID, err.Error(), http.StatusConflict)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/tasks/%d", taskID), http.StatusSeeOther)
}

func (s *Server) renderTaskPage(w http.ResponseWriter, taskID int64, answerError string, statusCode int) {
	task, err := s.db.GetTask(taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	logContent, _ := s.db.GetFullLog(taskID)
	proj, _ := s.db.GetProject(task.ProjectID)
	taskAttachments, _ := s.db.ListTaskAttachments(taskID)
	projectAttachments, _ := s.db.ListProjectAttachments(task.ProjectID)
	if statusCode > 0 {
		w.WriteHeader(statusCode)
	}
	s.renderTemplate(w, "task.html", map[string]any{
		"Task":         task,
		"Project":      proj,
		"Log":          logContent,
		"TaskFiles":    taskAttachments,
		"ProjectFiles": projectAttachments,
		"AnswerError":  answerError,
	})
}

func (s *Server) handleAttachmentDownload(w http.ResponseWriter, r *http.Request) {
	id, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	att, err := s.db.GetAttachment(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", att.Name))
	http.ServeFile(w, r, att.Path)
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := getIDParam(r, "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	att, err := s.db.GetAttachment(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if att.Path != "" {
		if err := os.Remove(att.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.db.DeleteAttachment(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	back := r.Referer()
	if back == "" {
		if att.TaskID != nil {
			back = fmt.Sprintf("/tasks/%d", *att.TaskID)
		} else if att.ProjectID != nil {
			back = fmt.Sprintf("/projects/%d", *att.ProjectID)
		} else {
			back = "/"
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
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

// --- Settings handlers ---

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.db.GetSettings()
	if settings == nil {
		settings = &db.Settings{}
	}
	s.renderTemplate(w, "settings.html", map[string]any{
		"Settings": settings,
		"Setup":    false,
	})
}

func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.db.GetSettings()
	if settings == nil {
		settings = &db.Settings{}
	}
	s.renderTemplate(w, "settings.html", map[string]any{
		"Settings": settings,
		"Setup":    true,
	})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("telegram_token"))
	var userID int64
	if v := strings.TrimSpace(r.FormValue("telegram_user_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid telegram user id", http.StatusBadRequest)
			return
		}
		userID = id
	}
	if err := s.db.UpsertSettings(token, userID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.daemon != nil {
		if err := s.daemon.UpdateTelegramSettings(token, userID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/setup") {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
		RuntimeMode string `json:"runtime_mode"`
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
	if req.RuntimeMode == "" {
		req.RuntimeMode = "docker"
	}
	req.RuntimeMode = normalizeRuntimeMode(req.RuntimeMode)
	proj, err := s.db.CreateProject(req.Name, req.RepoPath, req.DockerImage, req.Agent, false)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateProjectRuntimeMode(proj.ID, req.RuntimeMode); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj.RuntimeMode = req.RuntimeMode
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
		Profile     string `json:"profile"`
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
	if err := s.db.UpdateTaskProfile(task.ID, normalizeTaskProfile(req.Profile)); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	created, _ := s.db.GetTask(task.ID)
	jsonOK(w, created)
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
		Agent    string  `json:"agent"`
		Profile  *string `json:"profile"`
		Priority *int    `json:"priority"`
		Failover *bool   `json:"failover"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Agent != "" {
		s.db.UpdateTaskAgent(taskID, req.Agent)
	}
	if req.Profile != nil {
		s.db.UpdateTaskProfile(taskID, normalizeTaskProfile(*req.Profile))
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

func normalizeTaskProfile(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "default", "none":
		return ""
	case "architect", "developer", "qa", "refactorer", "incident", "ux-reviewer":
		return strings.TrimSpace(strings.ToLower(raw))
	default:
		return ""
	}
}

func normalizeRuntimeMode(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "docker":
		return "docker"
	case "host", "":
		return "host"
	default:
		return "host"
	}
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
