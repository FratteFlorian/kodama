package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
)

//go:embed templates/* static/*
var embedFS embed.FS

// DaemonController is the interface the web server uses to control the daemon.
type DaemonController interface {
	StartProject(ctx context.Context, projectID int64) error
	StopProject(projectID int64)
	IsRunning(projectID int64) bool
	AnswerQuestion(taskID int64, answer string) error
}

// Server is the HTTP server for Kodama's web UI.
type Server struct {
	cfg      *config.Config
	db       *db.DB
	hub      *Hub
	daemon   DaemonController
	router   chi.Router
	tmpl     *template.Template
}

// New creates and configures a new web Server.
func New(cfg *config.Config, database *db.DB, hub *Hub, daemon DaemonController) (*Server, error) {
	s := &Server{
		cfg:    cfg,
		db:     database,
		hub:    hub,
		daemon: daemon,
	}

	// Parse all templates.
	tmplFS, err := fs.Sub(embedFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("template fs: %w", err)
	}
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(tmplFS, "*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s.tmpl = tmpl

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

	// Static assets.
	staticFS, _ := fs.Sub(embedFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Web UI routes.
	r.Get("/", s.handleIndex)
	r.Post("/projects", s.handleCreateProject)
	r.Get("/projects/{id}", s.handleProject)
	r.Post("/projects/{id}/tasks", s.handleCreateTask)
	r.Put("/projects/{id}/tasks/{tid}", s.handleUpdateTask)
	r.Delete("/projects/{id}/tasks/{tid}", s.handleDeleteTask)
	r.Post("/projects/{id}/start", s.handleStartProject)
	r.Post("/projects/{id}/stop", s.handleStopProject)
	r.Get("/tasks/{id}", s.handleTask)
	r.Post("/tasks/{id}/answer", s.handleAnswerQuestion)
	r.Get("/ws/tasks/{id}", s.handleWebSocket)

	// JSON API (for TUI).
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
	}
}

// renderTemplate renders a named template with data to the response.
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
