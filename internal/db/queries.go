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

func (db *DB) CreateTask(projectID int64, description, agent string, priority int, failover bool) (*Task, error) {
	res, err := db.sql.Exec(
		`INSERT INTO tasks (project_id, description, agent, priority, failover) VALUES (?, ?, ?, ?, ?)`,
		projectID, description, agent, priority, boolToInt(failover),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.GetTask(id)
}

func (db *DB) GetTask(id int64) (*Task, error) {
	row := db.sql.QueryRow(
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at,
		        session_id, cost_usd, input_tokens, output_tokens, resume_question, resume_answer, failover FROM tasks WHERE id = ?`, id,
	)
	return scanTask(row)
}

func (db *DB) ListTasks(projectID int64) ([]*Task, error) {
	rows, err := db.sql.Query(
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at,
		        session_id, cost_usd, input_tokens, output_tokens, resume_question, resume_answer, failover
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
		`SELECT id, project_id, description, status, agent, priority, created_at, started_at, completed_at,
		        session_id, cost_usd, input_tokens, output_tokens, resume_question, resume_answer, failover
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

func (db *DB) UpdateTaskSessionID(id int64, sessionID string) error {
	_, err := db.sql.Exec(`UPDATE tasks SET session_id=? WHERE id=?`, sessionID, id)
	return err
}

func (db *DB) UpdateTaskCost(id int64, costUSD float64, inputTokens, outputTokens int64) error {
	_, err := db.sql.Exec(
		`UPDATE tasks SET cost_usd=?, input_tokens=?, output_tokens=? WHERE id=?`,
		costUSD, inputTokens, outputTokens, id,
	)
	return err
}

func (db *DB) UpdateTaskResume(id int64, question, answer string) error {
	_, err := db.sql.Exec(`UPDATE tasks SET resume_question=?, resume_answer=? WHERE id=?`, question, answer, id)
	return err
}

func (db *DB) ClearTaskResume(id int64) error {
	_, err := db.sql.Exec(`UPDATE tasks SET resume_question='', resume_answer='' WHERE id=?`, id)
	return err
}

func (db *DB) UpdateTaskDescription(id int64, description string) error {
	_, err := db.sql.Exec(`UPDATE tasks SET description=? WHERE id=?`, description, id)
	return err
}

func (db *DB) UpdateTaskAgent(id int64, agent string) error {
	_, err := db.sql.Exec(`UPDATE tasks SET agent=? WHERE id=?`, agent, id)
	return err
}

func (db *DB) UpdateTaskPriority(id int64, priority int) error {
	_, err := db.sql.Exec(`UPDATE tasks SET priority=? WHERE id=?`, priority, id)
	return err
}

func (db *DB) UpdateTaskFailover(id int64, failover bool) error {
	_, err := db.sql.Exec(`UPDATE tasks SET failover=? WHERE id=?`, boolToInt(failover), id)
	return err
}

func (db *DB) DeleteTask(id int64) error {
	_, err := db.sql.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

func (db *DB) NextTaskPriority(projectID int64) (int, error) {
	row := db.sql.QueryRow(`SELECT COALESCE(MAX(priority), 0) FROM tasks WHERE project_id = ?`, projectID)
	var max int
	if err := row.Scan(&max); err != nil {
		return 0, err
	}
	return max + 1, nil
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

// --- Environments ---

// GetEnvironment returns the environment for a project, or nil if none exists.
func (db *DB) GetEnvironment(projectID int64) (*Environment, error) {
	row := db.sql.QueryRow(
		`SELECT id, project_id, type, config_path, status, created_at, started_at, stopped_at
		 FROM environments WHERE project_id = ? LIMIT 1`, projectID,
	)
	return scanEnvironment(row)
}

// UpsertEnvironment creates or updates the environment config for a project.
func (db *DB) UpsertEnvironment(projectID int64, envType, configPath string) (*Environment, error) {
	existing, err := db.GetEnvironment(projectID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if _, err := db.sql.Exec(
			`UPDATE environments SET type=?, config_path=? WHERE id=?`,
			envType, configPath, existing.ID,
		); err != nil {
			return nil, err
		}
		return db.GetEnvironment(projectID)
	}
	res, err := db.sql.Exec(
		`INSERT INTO environments (project_id, type, config_path) VALUES (?, ?, ?)`,
		projectID, envType, configPath,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	row := db.sql.QueryRow(
		`SELECT id, project_id, type, config_path, status, created_at, started_at, stopped_at
		 FROM environments WHERE id = ?`, id,
	)
	return scanEnvironment(row)
}

// UpdateEnvironmentStatus updates the status (and timestamps) of an environment.
func (db *DB) UpdateEnvironmentStatus(id int64, status EnvironmentStatus) error {
	now := time.Now()
	switch status {
	case EnvironmentStatusRunning:
		_, err := db.sql.Exec(`UPDATE environments SET status=?, started_at=? WHERE id=?`, status, now, id)
		return err
	case EnvironmentStatusStopped, EnvironmentStatusError:
		_, err := db.sql.Exec(`UPDATE environments SET status=?, stopped_at=? WHERE id=?`, status, now, id)
		return err
	default:
		_, err := db.sql.Exec(`UPDATE environments SET status=? WHERE id=?`, status, id)
		return err
	}
}

// AppendEnvironmentLog appends a log chunk for an environment.
func (db *DB) AppendEnvironmentLog(envID int64, chunk string) error {
	_, err := db.sql.Exec(`INSERT INTO environment_logs (env_id, chunk) VALUES (?, ?)`, envID, chunk)
	return err
}

// GetEnvironmentLog returns the full accumulated log for an environment.
func (db *DB) GetEnvironmentLog(envID int64) (string, error) {
	rows, err := db.sql.Query(
		`SELECT chunk FROM environment_logs WHERE env_id = ? ORDER BY ts ASC, id ASC`, envID,
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

// --- Settings ---

func (db *DB) GetSettings() (*Settings, error) {
	row := db.sql.QueryRow(`SELECT telegram_token, telegram_user_id FROM settings WHERE id = 1`)
	var s Settings
	err := row.Scan(&s.TelegramToken, &s.TelegramUserID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (db *DB) UpsertSettings(telegramToken string, telegramUserID int64) error {
	_, err := db.sql.Exec(
		`INSERT INTO settings (id, telegram_token, telegram_user_id, updated_at)
		 VALUES (1, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET telegram_token=excluded.telegram_token,
		 telegram_user_id=excluded.telegram_user_id, updated_at=CURRENT_TIMESTAMP`,
		telegramToken, telegramUserID,
	)
	return err
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
		&t.CreatedAt, &startedAt, &completedAt,
		&t.SessionID, &t.CostUSD, &t.InputTokens, &t.OutputTokens,
		&t.ResumeQuestion, &t.ResumeAnswer, &t.Failover)
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

func scanEnvironment(s scanner) (*Environment, error) {
	var e Environment
	var startedAt, stoppedAt sql.NullTime
	err := s.Scan(&e.ID, &e.ProjectID, &e.Type, &e.ConfigPath, &e.Status, &e.CreatedAt, &startedAt, &stoppedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if startedAt.Valid {
		e.StartedAt = &startedAt.Time
	}
	if stoppedAt.Valid {
		e.StoppedAt = &stoppedAt.Time
	}
	return &e, nil
}
