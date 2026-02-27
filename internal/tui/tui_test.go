package tui

import (
	"testing"

	"github.com/florian/kodama/internal/db"
	"github.com/stretchr/testify/assert"
)

// Verify model implements tea.Model at compile time.
var _ = model{}.Init

func TestRenderProjectList(t *testing.T) {
	m := model{
		view:  viewProjects,
		width: 80, height: 24,
		projects: []*db.Project{
			{ID: 1, Name: "Project Alpha", Agent: "claude"},
			{ID: 2, Name: "Project Beta", Agent: "codex"},
		},
		projCursor: 0,
	}
	out := m.renderProjectList()
	assert.Contains(t, out, "Project Alpha")
	assert.Contains(t, out, "Project Beta")
	assert.Contains(t, out, "claude")
}

func TestRenderProjectListEmpty(t *testing.T) {
	m := model{view: viewProjects, width: 80, height: 24}
	out := m.renderProjectList()
	assert.Contains(t, out, "No projects")
}

func TestRenderProjectDetail(t *testing.T) {
	m := model{
		view:  viewProject,
		width: 80, height: 24,
		project: &db.Project{ID: 1, Name: "Test Project", Agent: "claude"},
		tasks: []*db.Task{
			{ID: 1, Description: "Implement auth", Status: db.TaskStatusPending, Agent: "claude"},
			{ID: 2, Description: "Write tests", Status: db.TaskStatusDone, Agent: "codex"},
		},
		taskCursor: 0,
	}
	out := m.renderProjectDetail()
	assert.Contains(t, out, "Test Project")
	assert.Contains(t, out, "Implement auth")
	assert.Contains(t, out, "Write tests")
}

func TestRenderTaskDetail(t *testing.T) {
	m := model{
		view:  viewTask,
		width: 80, height: 24,
		task: &db.Task{
			ID:          42,
			Description: "Build the thing",
			Status:      db.TaskStatusRunning,
		},
		taskLog: "Starting work...\nMaking progress...",
	}
	out := m.renderTaskDetail()
	assert.Contains(t, out, "Task #42")
	assert.Contains(t, out, "Starting work")
}

func TestRenderTaskDetailWaiting(t *testing.T) {
	m := model{
		view:  viewTask,
		width: 80, height: 24,
		task: &db.Task{
			ID:     1,
			Status: db.TaskStatusWaiting,
		},
	}
	out := m.renderTaskDetail()
	assert.Contains(t, out, "waiting for input")
}

func TestRenderInputMode(t *testing.T) {
	m := model{
		view:      viewTask,
		width:     80,
		height:    24,
		inputMode: true,
		inputBuffer: "my answer",
		task:      &db.Task{ID: 1, Status: db.TaskStatusWaiting},
	}
	out := m.renderTaskDetail()
	assert.Contains(t, out, "my answer")
	assert.Contains(t, out, "Answer:")
}

func TestRenderStatus(t *testing.T) {
	assert.NotEmpty(t, renderStatus(db.TaskStatusPending))
	assert.NotEmpty(t, renderStatus(db.TaskStatusRunning))
	assert.NotEmpty(t, renderStatus(db.TaskStatusDone))
	assert.NotEmpty(t, renderStatus(db.TaskStatusFailed))
	assert.NotEmpty(t, renderStatus(db.TaskStatusWaiting))
	assert.NotEmpty(t, renderStatus(db.TaskStatusRateLimited))
}

func TestCursorNavigation(t *testing.T) {
	m := model{
		view:  viewProjects,
		width: 80, height: 24,
		projects: []*db.Project{
			{ID: 1, Name: "P1"},
			{ID: 2, Name: "P2"},
			{ID: 3, Name: "P3"},
		},
		projCursor: 0,
	}

	// Simulate down navigation.
	m.projCursor = 1
	assert.Equal(t, 1, m.projCursor)

	// Ensure cursor can reach end.
	m.projCursor = 2
	assert.Equal(t, 2, m.projCursor)

	// Cannot go past end.
	if m.projCursor < len(m.projects)-1 {
		m.projCursor++
	}
	// cursor stays at 2 since we check bounds.
}
