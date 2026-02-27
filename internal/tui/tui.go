package tui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/florian/kodama/internal/db"
)

// msgs for async operations.
type projectsLoadedMsg struct{ projects []*db.Project }
type tasksLoadedMsg struct{ tasks []*db.Task }
type taskLoadedMsg struct{ task *db.Task }
type wsLineMsg struct{ line string }
type errMsg struct{ err error }

// Run starts the TUI and connects to the daemon at baseURL.
func Run(baseURL string) error {
	m := &model{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
		view:    viewProjects,
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Init implements tea.Model.
func (m model) Init() tea.Cmd {
	return m.loadProjects()
}

// Update implements tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case projectsLoadedMsg:
		m.projects = msg.projects
		m.err = nil
		return m, nil

	case tasksLoadedMsg:
		m.tasks = msg.tasks
		m.err = nil
		return m, nil

	case taskLoadedMsg:
		m.task = msg.task
		return m, nil

	case wsLineMsg:
		m.wsLines = append(m.wsLines, msg.line)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// handleKey dispatches key events based on current view.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Input mode (answering a question).
	if m.inputMode {
		return m.handleInputKey(msg)
	}

	switch m.view {
	case viewProjects:
		return m.handleProjectListKey(msg)
	case viewProject:
		return m.handleProjectDetailKey(msg)
	case viewTask:
		return m.handleTaskDetailKey(msg)
	}
	return m, nil
}

func (m model) handleProjectListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.projCursor > 0 {
			m.projCursor--
		}
	case "down", "j":
		if m.projCursor < len(m.projects)-1 {
			m.projCursor++
		}
	case "enter":
		if len(m.projects) > 0 {
			m.project = m.projects[m.projCursor]
			m.view = viewProject
			m.taskCursor = 0
			return m, m.loadTasks(m.project.ID)
		}
	case "r":
		return m, m.loadProjects()
	}
	return m, nil
}

func (m model) handleProjectDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "left", "h", "esc":
		m.view = viewProjects
		m.tasks = nil
		return m, m.loadProjects()
	case "up", "k":
		if m.taskCursor > 0 {
			m.taskCursor--
		}
	case "down", "j":
		if m.taskCursor < len(m.tasks)-1 {
			m.taskCursor++
		}
	case "enter":
		if len(m.tasks) > 0 {
			t := m.tasks[m.taskCursor]
			m.task = t
			m.wsLines = nil
			m.taskLog = ""
			m.view = viewTask
			return m, tea.Batch(m.loadTask(t.ID), m.pollTaskStatus(t.ID))
		}
	case "s":
		if m.project != nil {
			return m, m.startProjectCmd(m.project.ID)
		}
	case "d":
		if len(m.tasks) > 0 {
			t := m.tasks[m.taskCursor]
			return m, m.deleteTaskCmd(t.ID)
		}
	case "r":
		if m.project != nil {
			return m, m.loadTasks(m.project.ID)
		}
	}
	return m, nil
}

func (m model) handleTaskDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "left", "h", "esc":
		m.view = viewProject
		if m.project != nil {
			return m, m.loadTasks(m.project.ID)
		}
	case "i":
		if m.task != nil && m.task.Status == db.TaskStatusWaiting {
			m.inputMode = true
			m.inputBuffer = ""
		}
	case "r":
		if m.task != nil {
			return m, m.loadTask(m.task.ID)
		}
	}
	return m, nil
}

func (m model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputMode = false
		m.inputBuffer = ""
	case "enter":
		answer := m.inputBuffer
		m.inputMode = false
		m.inputBuffer = ""
		if m.task != nil {
			return m, m.answerQuestionCmd(m.task.ID, answer)
		}
	case "backspace":
		if len(m.inputBuffer) > 0 {
			m.inputBuffer = m.inputBuffer[:len(m.inputBuffer)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.inputBuffer += msg.String()
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m model) View() string {
	switch m.view {
	case viewProjects:
		return m.renderProjectList()
	case viewProject:
		return m.renderProjectDetail()
	case viewTask:
		return m.renderTaskDetail()
	}
	return ""
}

// --- Commands ---

func (m *model) loadProjects() tea.Cmd {
	return func() tea.Msg {
		projects, err := m.fetchProjects()
		if err != nil {
			return errMsg{err}
		}
		return projectsLoadedMsg{projects}
	}
}

func (m *model) loadTasks(projectID int64) tea.Cmd {
	return func() tea.Msg {
		tasks, err := m.fetchTasks(projectID)
		if err != nil {
			return errMsg{err}
		}
		return tasksLoadedMsg{tasks}
	}
}

func (m *model) loadTask(taskID int64) tea.Cmd {
	return func() tea.Msg {
		task, err := m.fetchTask(taskID)
		if err != nil {
			return errMsg{err}
		}
		return taskLoadedMsg{task}
	}
}

func (m *model) pollTaskStatus(taskID int64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		task, err := m.fetchTask(taskID)
		if err != nil {
			return errMsg{err}
		}
		return taskLoadedMsg{task}
	}
}

func (m *model) startProjectCmd(projectID int64) tea.Cmd {
	return func() tea.Msg {
		err := m.startProject(projectID)
		if err != nil {
			return errMsg{err}
		}
		tasks, err := m.fetchTasks(projectID)
		if err != nil {
			return errMsg{err}
		}
		return tasksLoadedMsg{tasks}
	}
}

func (m *model) deleteTaskCmd(taskID int64) tea.Cmd {
	pid := int64(0)
	if m.project != nil {
		pid = m.project.ID
	}
	return func() tea.Msg {
		m.deleteTask(taskID)
		if pid != 0 {
			tasks, err := m.fetchTasks(pid)
			if err != nil {
				return errMsg{err}
			}
			return tasksLoadedMsg{tasks}
		}
		return nil
	}
}

func (m *model) answerQuestionCmd(taskID int64, answer string) tea.Cmd {
	return func() tea.Msg {
		err := m.answerQuestion(taskID, answer)
		if err != nil {
			return errMsg{err}
		}
		task, err := m.fetchTask(taskID)
		if err != nil {
			return errMsg{err}
		}
		return taskLoadedMsg{task}
	}
}

// Ensure model implements tea.Model.
var _ tea.Model = model{}

// statusLineHelp returns a help string.
func statusLineHelp(v view) string {
	switch v {
	case viewProjects:
		return "↑/↓ navigate  Enter select  r refresh  q quit"
	case viewProject:
		return "↑/↓ navigate  Enter view  s start  d delete  ← back  q quit"
	case viewTask:
		return "i answer  r refresh  ← back  q quit"
	}
	return strings.Repeat(" ", 40)
}

// Prevent unused import.
var _ = fmt.Sprintf
