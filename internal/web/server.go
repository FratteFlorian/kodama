package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed templates/* static/*
var embedFS embed.FS

// DaemonController is the interface the web server uses to control the daemon.
type DaemonController interface {
	StartProject(ctx context.Context, projectID int64) error
	StopProject(projectID int64)
	IsRunning(projectID int64) bool
	AnswerQuestion(taskID int64, answer string) error
	// Environment control.
	StartEnvironment(ctx context.Context, projectID int64) error
	StopEnvironment(projectID int64) error
	RestartEnvironment(ctx context.Context, projectID int64) error
	IsEnvRunning(projectID int64) bool
	UpdateTelegramSettings(token string, userID int64) error
}

// Server is the HTTP server for Kodama's web UI.
type Server struct {
	cfg       *config.Config
	db        *db.DB
	hub       *Hub
	envHub    *Hub // for environment WebSocket streams
	daemon    DaemonController
	router    chi.Router
	templates map[string]*template.Template
}

// New creates and configures a new web Server.
func New(cfg *config.Config, database *db.DB, hub *Hub, envHub *Hub, daemon DaemonController) (*Server, error) {
	s := &Server{
		cfg:    cfg,
		db:     database,
		hub:    hub,
		envHub: envHub,
		daemon: daemon,
	}

	tmplFS, err := fs.Sub(embedFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("template fs: %w", err)
	}

	// Parse each page as its own template set (layout + page).
	// This prevents {{define "content"}} blocks from conflicting across pages.
	pages := []string{"index.html", "project.html", "task.html", "environment.html", "settings.html", "files.html"}
	s.templates = make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New("").Funcs(templateFuncs()).ParseFS(tmplFS, "layout.html", page)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}
		s.templates[page] = t
	}

	s.router = s.buildRouter()
	return s, nil
}

// Handler returns the http.Handler for the server.
func (s *Server) Handler() http.Handler {
	return s.router
}

// buildRouter sets up all routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(s.setupGate)

	// Static assets.
	staticFS, _ := fs.Sub(embedFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Web UI routes.
	r.Get("/", s.handleIndex)
	r.Post("/projects", s.handleCreateProject)
	r.Get("/projects/{id}", s.handleProject)
	r.Get("/projects/{id}/files", s.handleProjectFiles)
	r.Get("/projects/{id}/files/raw", s.handleProjectFileRaw)
	r.Post("/projects/{id}/tasks", s.handleCreateTask)
	r.Post("/projects/{id}/tasks/plan", s.handlePlanTasksFromPRD)
	r.Post("/projects/{id}/tasks/workflow", s.handleCreateWorkflowTasks)
	// HTML forms can only GET/POST, so expose dedicated POST routes for update/delete.
	r.Post("/projects/{id}/tasks/{tid}/delete", s.handleDeleteTask)
	r.Post("/projects/{id}/tasks/{tid}/agent", s.handleUpdateTaskAgent)
	r.Post("/projects/{id}/tasks/{tid}/profile", s.handleUpdateTaskProfile)
	r.Post("/projects/{id}/tasks/{tid}/failover", s.handleUpdateTaskFailover)
	r.Post("/projects/{id}/tasks/{tid}/retry", s.handleRetryTask)
	// Keep REST routes for the JSON API.
	r.Put("/projects/{id}/tasks/{tid}", s.handleUpdateTask)
	r.Delete("/projects/{id}/tasks/{tid}", s.handleDeleteTask)
	r.Post("/projects/{id}/settings", s.handleUpdateProjectSettings)
	r.Post("/projects/{id}/docker/recreate", s.handleRecreateDockerFiles)
	r.Post("/projects/{id}/start", s.handleStartProject)
	r.Post("/projects/{id}/stop", s.handleStopProject)
	r.Get("/tasks/{id}", s.handleTask)
	r.Post("/tasks/{id}/answer", s.handleAnswerQuestion)
	r.Get("/attachments/{id}", s.handleAttachmentDownload)
	r.Post("/attachments/{id}/delete", s.handleDeleteAttachment)
	r.Get("/ws/tasks/{id}", s.handleWebSocket)
	r.Get("/settings", s.handleSettingsPage)
	r.Post("/settings", s.handleSettingsSave)
	r.Get("/setup", s.handleSetupPage)
	r.Post("/setup", s.handleSettingsSave)

	// Dev environment routes.
	r.Get("/projects/{id}/environment", s.handleEnvironmentPage)
	r.Post("/projects/{id}/environment", s.handleEnvironmentConfigure)
	r.Post("/projects/{id}/environment/start", s.handleEnvironmentStart)
	r.Post("/projects/{id}/environment/stop", s.handleEnvironmentStop)
	r.Post("/projects/{id}/environment/restart", s.handleEnvironmentRestart)
	r.Get("/ws/environment/{id}", s.handleEnvironmentWebSocket)

	// JSON API.
	r.Get("/api/projects", s.apiListProjects)
	r.Get("/api/projects/{id}", s.apiGetProject)
	r.Post("/api/projects", s.apiCreateProject)
	r.Get("/api/projects/{id}/tasks", s.apiListTasks)
	r.Post("/api/projects/{id}/tasks", s.apiCreateTask)
	r.Get("/api/tasks/{id}", s.apiGetTask)
	r.Put("/api/tasks/{id}", s.apiUpdateTask)
	r.Delete("/api/tasks/{id}", s.apiDeleteTask)
	r.Post("/api/projects/{id}/start", s.apiStartProject)
	r.Post("/api/tasks/{id}/answer", s.apiAnswerQuestion)

	return r
}

// templateFuncs returns custom template functions.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"statusBadge": func(status db.TaskStatus) string {
			switch status {
			case db.TaskStatusRunning:
				return "running"
			case db.TaskStatusWaiting:
				return "waiting"
			case db.TaskStatusDone:
				return "done"
			case db.TaskStatusFailed:
				return "failed"
			case db.TaskStatusRateLimited:
				return "rate-limited"
			default:
				return "pending"
			}
		},
		"formatTokens": func(n int64) string {
			if n >= 1_000_000 {
				return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
			}
			if n >= 1000 {
				return fmt.Sprintf("%.1fk", float64(n)/1000)
			}
			return fmt.Sprintf("%d", n)
		},
		"envStatusBadge": func(status db.EnvironmentStatus) string {
			switch status {
			case db.EnvironmentStatusRunning:
				return "running"
			case db.EnvironmentStatusStarting:
				return "starting"
			case db.EnvironmentStatusStopping:
				return "stopping"
			case db.EnvironmentStatusError:
				return "failed"
			default:
				return "stopped"
			}
		},
	}
}

// renderTemplate renders a named template with data to the response.
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	// Buffer the render so we can return a proper 500 on error
	// without writing partial HTML to the client.
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		slog.Error("render template", "name", name, "err", err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(buf.String()))
}
