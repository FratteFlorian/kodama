package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	taskPlanBegin = "KODAMA_TASKS_BEGIN"
	taskPlanEnd   = "KODAMA_TASKS_END"
)

type plannedTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Profile     string `json:"profile"`
	Agent       string `json:"agent"`
	Failover    bool   `json:"failover"`
}

func extractPlannedTasks(output string) ([]plannedTask, error) {
	searchFrom := 0
	foundBlock := false
	var lastValid []plannedTask
	var lastErr error

	for {
		startRel := strings.Index(output[searchFrom:], taskPlanBegin)
		if startRel < 0 {
			break
		}
		start := searchFrom + startRel + len(taskPlanBegin)
		endRel := strings.Index(output[start:], taskPlanEnd)
		if endRel < 0 {
			return nil, fmt.Errorf("task plan end marker missing")
		}
		end := start + endRel

		block := strings.TrimSpace(output[start:end])
		block = strings.Trim(block, "`")
		block = strings.TrimSpace(block)
		foundBlock = true
		if block != "" {
			var tasks []plannedTask
			if err := json.Unmarshal([]byte(block), &tasks); err != nil {
				lastErr = fmt.Errorf("parse planned tasks JSON: %w", err)
			} else {
				lastValid = tasks
				lastErr = nil
			}
		}

		searchFrom = end + len(taskPlanEnd)
	}

	if !foundBlock {
		return nil, nil
	}
	if lastErr != nil && lastValid == nil {
		return nil, lastErr
	}
	return lastValid, nil
}

func normalizePlannedAgent(agent string) string {
	switch strings.TrimSpace(strings.ToLower(agent)) {
	case "", "claude", "codex":
		return strings.TrimSpace(strings.ToLower(agent))
	default:
		return ""
	}
}

func normalizePlannedProfile(profile string) string {
	switch strings.TrimSpace(strings.ToLower(profile)) {
	case "", "architect", "developer", "qa", "refactorer", "incident", "ux-reviewer":
		return strings.TrimSpace(strings.ToLower(profile))
	default:
		return ""
	}
}

func buildTaskDescription(p plannedTask) string {
	title := strings.TrimSpace(p.Title)
	desc := strings.TrimSpace(p.Description)
	switch {
	case title == "" && desc == "":
		return ""
	case title == "":
		return desc
	case desc == "":
		return title
	default:
		return title + "\n\n" + desc
	}
}

func (d *Daemon) importPlannedTasks(projectID int64, planned []plannedTask) (int, error) {
	if len(planned) == 0 {
		return 0, nil
	}
	existing, err := d.db.ListTasks(projectID)
	if err != nil {
		return 0, err
	}
	existingDesc := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		existingDesc[strings.TrimSpace(t.Description)] = struct{}{}
	}

	type idxTask struct {
		idx int
		t   plannedTask
	}
	ordered := make([]idxTask, 0, len(planned))
	for i, t := range planned {
		ordered = append(ordered, idxTask{idx: i, t: t})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		pi := ordered[i].t.Priority
		pj := ordered[j].t.Priority
		if pi == pj {
			return ordered[i].idx < ordered[j].idx
		}
		return pi < pj
	})

	nextPriority, err := d.db.NextTaskPriority(projectID)
	if err != nil {
		return 0, err
	}
	imported := 0
	for _, entry := range ordered {
		desc := buildTaskDescription(entry.t)
		if desc == "" {
			continue
		}
		if _, ok := existingDesc[desc]; ok {
			continue
		}
		task, err := d.db.CreateTask(projectID, desc, normalizePlannedAgent(entry.t.Agent), nextPriority, entry.t.Failover)
		if err != nil {
			return imported, err
		}
		if err := d.db.UpdateTaskProfile(task.ID, normalizePlannedProfile(entry.t.Profile)); err != nil {
			return imported, err
		}
		existingDesc[desc] = struct{}{}
		nextPriority++
		imported++
	}
	return imported, nil
}
