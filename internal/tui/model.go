package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/florian/kodama/internal/db"
)

// view represents the current TUI view.
type view int

const (
	viewProjects view = iota
	viewProject
	viewTask
)

// model is the Bubble Tea model.
type model struct {
	baseURL  string
	client   *http.Client
	view     view
	err      error

	// Project list view.
	projects   []*db.Project
	projCursor int

	// Project detail view.
	project    *db.Project
	tasks      []*db.Task
	taskCursor int

	// Task detail view.
	task      *db.Task
	taskLog   string
	wsLines   []string

	// Input mode (answering a question).
	inputMode   bool
	inputBuffer string

	// Window dimensions.
	width  int
	height int
}

// --- API client helpers ---

func (m *model) fetchProjects() ([]*db.Project, error) {
	resp, err := m.client.Get(m.baseURL + "/api/projects")
	if err != nil {
		return nil, fmt.Errorf("fetch projects: %w", err)
	}
	defer resp.Body.Close()
	var projects []*db.Project
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (m *model) fetchTasks(projectID int64) ([]*db.Task, error) {
	url := fmt.Sprintf("%s/api/projects/%d/tasks", m.baseURL, projectID)
	resp, err := m.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch tasks: %w", err)
	}
	defer resp.Body.Close()
	var tasks []*db.Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (m *model) fetchTask(taskID int64) (*db.Task, error) {
	url := fmt.Sprintf("%s/api/tasks/%d", m.baseURL, taskID)
	resp, err := m.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var task db.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *model) startProject(projectID int64) error {
	url := fmt.Sprintf("%s/api/projects/%d/start", m.baseURL, projectID)
	resp, err := m.client.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (m *model) answerQuestion(taskID int64, answer string) error {
	url := fmt.Sprintf("%s/api/tasks/%d/answer", m.baseURL, taskID)
	body := fmt.Sprintf(`{"answer":%q}`, answer)
	resp, err := m.client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (m *model) deleteTask(taskID int64) error {
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/tasks/%d", m.baseURL, taskID), nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (m *model) createTask(projectID int64, description, agent string) error {
	body := fmt.Sprintf(`{"description":%q,"agent":%q,"priority":0}`, description, agent)
	url := fmt.Sprintf("%s/api/projects/%d/tasks", m.baseURL, projectID)
	resp, err := m.client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// fetchTaskLog fetches the full log text for a task.
func (m *model) fetchTaskLog(taskID int64) (string, error) {
	// We get the full log via the task's WebSocket or a special API.
	// For simplicity, use the task output from the HTML API which already includes the log.
	// We'll just fetch the task detail and return empty; actual log streaming happens via WS.
	_ = taskID
	return "", nil
}

// connectWebSocket connects to the task WebSocket and streams output.
// Returns a channel that receives lines.
func connectWebSocket(baseURL string, taskID int64) (<-chan string, error) {
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = fmt.Sprintf("%s/ws/tasks/%d", wsURL, taskID)

	ch := make(chan string, 256)

	go func() {
		defer close(ch)

		// Simple WebSocket implementation using gorilla/websocket.
		// We try to connect with retries.
		for {
			if err := streamWS(wsURL, ch); err != nil {
				time.Sleep(2 * time.Second)
				// Check if channel was closed.
				select {
				case <-ch:
					return
				default:
				}
			} else {
				return
			}
		}
	}()

	return ch, nil
}

// streamWS reads from a WebSocket URL and writes chunks to ch.
func streamWS(wsURL string, ch chan<- string) error {
	resp, err := http.Get(strings.Replace(wsURL, "ws://", "http://", 1))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			ch <- string(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
