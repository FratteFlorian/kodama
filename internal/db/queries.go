package db

import (
	"database/sql"
	"fmt"
	"time"
)

// --- Projects ---

func (db *DB) CreateProject(name, repoPath, dockerImage, agent string, failover bool) (*Project, error) {
	res, err := db.sql.Exec(
		`INSERT INTO projects (name, repo_path, docker_image, agent, failover) VALUES (?, ?, ?, ?, ?)`,
		name, repoPath, dockerImage, agent, boolToInt(failover),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.GetProject(id)
}

func (db *DB) GetProject(id int64) (*Project, error) {
	row := db.sql.QueryRow(
		`SELECT id, name, repo_path, docker_image, agent, failover, created_at FROM projects WHERE id = ?`, id,
	)
	return scanProject(row)
}

func (db *DB) ListProjects() ([]*Project, error) {
	rows, err := db.sql.Query(
		`SELECT id, name, repo_path, docker_image, agent, failover, created_at FROM projects ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (db *DB) UpdateProject(id int64, name, repoPath, dockerImage, agent string, failover bool) error {
	_, err := db.sql.Exec(
		`UPDATE projects SET name=?, repo_path=?, docker_image=?, agent=?, failover=? WHERE id=?`,
		name, repoPath, dockerImage, agent, boolToInt(failover), id,
	)
	return err
}

func (db *DB) DeleteProject(id int64) error {
	_, err := db.sql.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// --- Tasks ---

func (db *DB) CreateTask(projectID int64, description, agent string, priority int) (*Task, error) {
	res, err := db.sql.Exec(
		`INSERT INTO tasks (project_id, description, agent, priority) VALUES (?, ?, ?, ?)`,
		projectID, description, agent, priority,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.GetTask(id)
}

func (db *DB) GetTask(id int64) (*Task, error) {
	row := db.sql.QueryRow(
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at FROM tasks WHERE id = ?`, id,
	)
	return scanTask(row)
}

func (db *DB) ListTasks(projectID int64) ([]*Task, error) {
	rows, err := db.sql.Query(
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at
		 FROM tasks WHERE project_id = ? ORDER BY priority ASC, created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (db *DB) ListPendingTasks(projectID int64) ([]*Task, error) {
	rows, err := db.sql.Query(
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at
		 FROM tasks WHERE project_id = ? AND status IN ('pending', 'rate_limited')
		 ORDER BY priority ASC, created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (db *DB) UpdateTaskStatus(id int64, status TaskStatus) error {
	now := time.Now()
	switch status {
	case TaskStatusRunning:
		_, err := db.sql.Exec(`UPDATE tasks SET status=?, started_at=? WHERE id=?`, status, now, id)
		return err
	case TaskStatusDone, TaskStatusFailed:
		_, err := db.sql.Exec(`UPDATE tasks SET status=?, completed_at=? WHERE id=?`, status, now, id)
		return err
	default:
		_, err := db.sql.Exec(`UPDATE tasks SET status=? WHERE id=?`, status, id)
		return err
	}
}

func (db *DB) UpdateTaskAgent(id int64, agent string) error {
	_, err := db.sql.Exec(`UPDATE tasks SET agent=? WHERE id=?`, agent, id)
	return err
}

func (db *DB) UpdateTaskPriority(id int64, priority int) error {
	_, err := db.sql.Exec(`UPDATE tasks SET priority=? WHERE id=?`, priority, id)
	return err
}

func (db *DB) DeleteTask(id int64) error {
	_, err := db.sql.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

// --- Task Logs ---

func (db *DB) AppendTaskLog(taskID int64, chunk string) error {
	_, err := db.sql.Exec(`INSERT INTO task_logs (task_id, chunk) VALUES (?, ?)`, taskID, chunk)
	return err
}

func (db *DB) GetFullLog(taskID int64) (string, error) {
	rows, err := db.sql.Query(
		`SELECT chunk FROM task_logs WHERE task_id = ? ORDER BY ts ASC, id ASC`, taskID,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var full string
	for rows.Next() {
		var chunk string
		if err := rows.Scan(&chunk); err != nil {
			return "", err
		}
		full += chunk
	}
	return full, rows.Err()
}

func (db *DB) GetTaskLogs(taskID int64) ([]*TaskLog, error) {
	rows, err := db.sql.Query(
		`SELECT id, task_id, chunk, ts FROM task_logs WHERE task_id = ? ORDER BY ts ASC, id ASC`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []*TaskLog
	for rows.Next() {
		var l TaskLog
		if err := rows.Scan(&l.ID, &l.TaskID, &l.Chunk, &l.Ts); err != nil {
			return nil, err
		}
		logs = append(logs, &l)
	}
	return logs, rows.Err()
}

// --- Task Checkpoints ---

func (db *DB) SaveCheckpoint(taskID int64, checklistState string) error {
	_, err := db.sql.Exec(
		`INSERT INTO task_checkpoints (task_id, checklist_state) VALUES (?, ?)`,
		taskID, checklistState,
	)
	return err
}

func (db *DB) GetLatestCheckpoint(taskID int64) (*TaskCheckpoint, error) {
	row := db.sql.QueryRow(
		`SELECT id, task_id, checklist_state, created_at FROM task_checkpoints WHERE task_id = ? ORDER BY id DESC LIMIT 1`,
		taskID,
	)
	var cp TaskCheckpoint
	err := row.Scan(&cp.ID, &cp.TaskID, &cp.ChecklistState, &cp.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cp, nil
}

// --- Helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(s scanner) (*Project, error) {
	var p Project
	var failover int
	err := s.Scan(&p.ID, &p.Name, &p.RepoPath, &p.DockerImage, &p.Agent, &failover, &p.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	p.Failover = failover != 0
	return &p, nil
}

func scanTask(s scanner) (*Task, error) {
	var t Task
	var startedAt, completedAt sql.NullTime
	err := s.Scan(&t.ID, &t.ProjectID, &t.Description, &t.Status, &t.Agent, &t.Priority,
		&t.CreatedAt, &startedAt, &completedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
	}
	if startedAt.Valid {
		t.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		t.CompletedAt = &completedAt.Time
	}
	return &t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
