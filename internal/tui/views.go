package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/florian/kodama/internal/db"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	styleSelected = lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("255"))
	styleMuted    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleGreen    = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	styleYellow   = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleRed      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleBlue     = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	styleBold     = lipgloss.NewStyle().Bold(true)
)

// renderProjectList renders the project list view.
func (m model) renderProjectList() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("🌿 Kodama — Projects"))
	b.WriteString("\n\n")

	if len(m.projects) == 0 {
		b.WriteString(styleMuted.Render("No projects. Create one via the web UI."))
	} else {
		for i, p := range m.projects {
			line := fmt.Sprintf("  %s  %s", styleMuted.Render(fmt.Sprintf("[%s]", p.Agent)), p.Name)
			if i == m.projCursor {
				line = styleSelected.Render(fmt.Sprintf("▶ %s  %s", styleMuted.Render(fmt.Sprintf("[%s]", p.Agent)), p.Name))
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("↑/↓ navigate  Enter select  q quit"))

	if m.err != nil {
		b.WriteString("\n" + styleRed.Render("Error: "+m.err.Error()))
	}

	return b.String()
}

// renderProjectDetail renders the project detail / backlog view.
func (m model) renderProjectDetail() string {
	var b strings.Builder

	if m.project == nil {
		return "Loading..."
	}

	b.WriteString(styleTitle.Render("🌿 " + m.project.Name))
	b.WriteString("  " + styleMuted.Render(m.project.RepoPath))
	b.WriteString("\n\n")

	if len(m.tasks) == 0 {
		b.WriteString(styleMuted.Render("No tasks in backlog."))
	} else {
		b.WriteString(styleBold.Render("Backlog"))
		b.WriteString("\n")
		for i, t := range m.tasks {
			statusStr := renderStatus(t.Status)
			agentStr := styleMuted.Render(fmt.Sprintf("[%s]", agentDisplay(t, m.project)))
			desc := t.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}

			line := fmt.Sprintf("  %s %s %s", statusStr, agentStr, desc)
			if i == m.taskCursor {
				line = fmt.Sprintf("▶ %s %s %s", statusStr, agentStr, desc)
				line = styleSelected.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleMuted.Render("↑/↓ navigate  Enter view  s start  d delete  ← back  q quit"))

	if m.err != nil {
		b.WriteString("\n" + styleRed.Render("Error: "+m.err.Error()))
	}

	return b.String()
}

// renderTaskDetail renders the task output view.
func (m model) renderTaskDetail() string {
	var b strings.Builder

	if m.task == nil {
		return "Loading..."
	}

	title := fmt.Sprintf("Task #%d — %s", m.task.ID, m.task.Description)
	if len(title) > m.width-4 {
		title = title[:m.width-7] + "..."
	}
	b.WriteString(styleTitle.Render(title))
	b.WriteString("  " + renderStatus(m.task.Status))
	b.WriteString("\n\n")

	if m.inputMode {
		b.WriteString(styleYellow.Render("Answer: "))
		b.WriteString(m.inputBuffer)
		b.WriteString("█")
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("Enter to submit  Esc to cancel"))
		return b.String()
	}

	if m.task.Status == db.TaskStatusWaiting {
		b.WriteString(styleYellow.Render("⚠ Agent is waiting for input — press 'i' to answer"))
		b.WriteString("\n\n")
	}

	// Show last N lines of log that fit in the terminal.
	maxLines := m.height - 8
	if maxLines < 5 {
		maxLines = 5
	}

	allLines := strings.Split(m.taskLog, "\n")
	wsLines := m.wsLines
	combined := append(allLines, wsLines...)

	if len(combined) > maxLines {
		combined = combined[len(combined)-maxLines:]
	}

	b.WriteString(strings.Join(combined, "\n"))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("← back  i answer  q quit"))

	if m.err != nil {
		b.WriteString("\n" + styleRed.Render("Error: "+m.err.Error()))
	}

	return b.String()
}

func renderStatus(s db.TaskStatus) string {
	switch s {
	case db.TaskStatusRunning:
		return styleBlue.Render("●")
	case db.TaskStatusWaiting:
		return styleYellow.Render("?")
	case db.TaskStatusDone:
		return styleGreen.Render("✓")
	case db.TaskStatusFailed:
		return styleRed.Render("✗")
	case db.TaskStatusRateLimited:
		return styleYellow.Render("⏸")
	default:
		return styleMuted.Render("○")
	}
}

func agentDisplay(t *db.Task, proj *db.Project) string {
	if t.Agent != "" {
		return t.Agent
	}
	if proj != nil {
		return proj.Agent
	}
	return "claude"
}
