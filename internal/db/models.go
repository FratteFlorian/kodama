package db

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending     TaskStatus = "pending"
	TaskStatusRunning     TaskStatus = "running"
	TaskStatusWaiting     TaskStatus = "waiting"
	TaskStatusRateLimited TaskStatus = "rate_limited"
	TaskStatusDone        TaskStatus = "done"
	TaskStatusFailed      TaskStatus = "failed"
)

// Project represents a managed coding project.
type Project struct {
	ID          int64
	Name        string
	RepoPath    string
	DockerImage string
	Agent       string // "claude" or "codex"
	Failover    bool
	CreatedAt   time.Time
}

// Task represents a backlog item to be processed.
type Task struct {
	ID           int64
	ProjectID    int64
	Description  string
	Status       TaskStatus
	Agent        string // overrides project default if set
	Priority     int
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	SessionID    string
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
}

// TaskLog represents a streamed output chunk from an agent.
type TaskLog struct {
	ID     int64
	TaskID int64
	Chunk  string
	Ts     time.Time
}

// TaskCheckpoint stores checklist state for rate-limit resume.
type TaskCheckpoint struct {
	ID             int64
	TaskID         int64
	ChecklistState string
	CreatedAt      time.Time
}

// EnvironmentStatus represents the lifecycle state of a dev environment.
type EnvironmentStatus string

const (
	EnvironmentStatusStopped  EnvironmentStatus = "stopped"
	EnvironmentStatusStarting EnvironmentStatus = "starting"
	EnvironmentStatusRunning  EnvironmentStatus = "running"
	EnvironmentStatusStopping EnvironmentStatus = "stopping"
	EnvironmentStatusError    EnvironmentStatus = "error"
)

// Environment represents a persistent dev environment for a project.
type Environment struct {
	ID         int64
	ProjectID  int64
	Type       string // "compose" or "dockerfile"
	ConfigPath string
	Status     EnvironmentStatus
	CreatedAt  time.Time
	StartedAt  *time.Time
	StoppedAt  *time.Time
}
