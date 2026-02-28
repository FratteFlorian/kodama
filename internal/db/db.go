package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the SQLite database at the given path and runs migrations.
func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, "kodama.db")
	sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite only supports one writer at a time; serialise through one connection.
	sqlDB.SetMaxOpenConns(1)
	// Enable foreign key support (must be done per-connection in SQLite).
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.sql.Close()
}

// migrate creates all tables if they don't exist.
func (db *DB) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS projects (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    repo_path    TEXT NOT NULL DEFAULT '',
    docker_image TEXT NOT NULL DEFAULT '',
    agent        TEXT NOT NULL DEFAULT 'claude',
    failover     INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    description  TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    agent        TEXT NOT NULL DEFAULT '',
    priority     INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at   DATETIME,
    completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS task_logs (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    chunk   TEXT NOT NULL,
    ts      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS task_checkpoints (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id          INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    checklist_state  TEXT NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS environments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type          TEXT NOT NULL DEFAULT 'compose',
    config_path   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'stopped',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at    DATETIME,
    stopped_at    DATETIME
);

CREATE TABLE IF NOT EXISTS environment_logs (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    env_id  INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    chunk   TEXT NOT NULL,
    ts      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_task_logs_task_id ON task_logs(task_id);
CREATE INDEX IF NOT EXISTS idx_environments_project_id ON environments(project_id);
CREATE INDEX IF NOT EXISTS idx_environment_logs_env_id ON environment_logs(env_id);
`
	if _, err := db.sql.Exec(schema); err != nil {
		return err
	}
	// Additive migrations: add columns to existing tables.
	// SQLite errors on duplicate column names — ignore those errors.
	migrations := []string{
		`ALTER TABLE tasks ADD COLUMN session_id    TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN cost_usd      REAL    NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN input_tokens  INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN resume_question TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN resume_answer   TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		db.sql.Exec(m) // ignore error: "duplicate column name" is expected on re-open
	}
	return nil
}
