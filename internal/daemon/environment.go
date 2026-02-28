package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/florian/kodama/internal/db"
)

// EnvironmentManager manages persistent dev environments (Docker containers) per project.
type EnvironmentManager struct {
	database *db.DB
	hub      Broadcaster
	mu       sync.Mutex
	running  map[int64]context.CancelFunc // envID → cancel
	done     map[int64]chan struct{}       // envID → closed when goroutine exits
}

// NewEnvironmentManager creates a new EnvironmentManager.
func NewEnvironmentManager(database *db.DB, hub Broadcaster) *EnvironmentManager {
	return &EnvironmentManager{
		database: database,
		hub:      hub,
		running:  make(map[int64]context.CancelFunc),
		done:     make(map[int64]chan struct{}),
	}
}

// IsRunning returns true if the environment with the given envID is currently running.
func (m *EnvironmentManager) IsRunning(envID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[envID]
	return ok
}

// ActiveEnv returns the running environment for a project, or nil if not running.
func (m *EnvironmentManager) ActiveEnv(projectID int64) *db.Environment {
	env, err := m.database.GetEnvironment(projectID)
	if err != nil || env == nil {
		return nil
	}
	if !m.IsRunning(env.ID) {
		return nil
	}
	return env
}

// Start launches the dev environment for the given env record asynchronously.
// repoPath is the project's repo directory (used as working dir and for volume mounts).
func (m *EnvironmentManager) Start(ctx context.Context, env *db.Environment, repoPath string) error {
	m.mu.Lock()
	if _, running := m.running[env.ID]; running {
		m.mu.Unlock()
		return fmt.Errorf("environment %d is already running", env.ID)
	}
	envCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	m.running[env.ID] = cancel
	m.done[env.ID] = doneCh
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, env.ID)
			if ch, ok := m.done[env.ID]; ok {
				close(ch)
				delete(m.done, env.ID)
			}
			m.mu.Unlock()
		}()
		m.runEnvironment(envCtx, env, repoPath)
	}()

	return nil
}

// StopAndWait cancels a running environment and waits for teardown to complete.
func (m *EnvironmentManager) StopAndWait(envID int64) {
	m.mu.Lock()
	cancel, ok := m.running[envID]
	doneCh := m.done[envID]
	m.mu.Unlock()

	if !ok {
		return
	}
	cancel()
	if doneCh != nil {
		<-doneCh
	}
}

// Stop cancels a running environment (does not wait for teardown).
func (m *EnvironmentManager) Stop(envID int64) error {
	m.mu.Lock()
	cancel, ok := m.running[envID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("environment %d is not running", envID)
	}
	cancel()
	return nil
}

// runEnvironment manages the full docker lifecycle in a goroutine.
func (m *EnvironmentManager) runEnvironment(ctx context.Context, env *db.Environment, repoPath string) {
	slog.Info("starting environment", "env_id", env.ID, "type", env.Type, "config", env.ConfigPath)
	m.logChunk(env.ID, fmt.Sprintf("[starting %s environment: %s]\n", env.Type, env.ConfigPath))
	m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusStarting)

	var startErr error
	switch env.Type {
	case "compose":
		startErr = m.startCompose(ctx, env, repoPath)
	case "dockerfile":
		startErr = m.startDockerfile(ctx, env, repoPath)
	default:
		m.logChunk(env.ID, fmt.Sprintf("[error] unknown environment type: %s\n", env.Type))
		m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusError)
		return
	}

	if startErr != nil {
		if ctx.Err() != nil {
			// Cancelled by user during startup — treat as stopped.
			m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusStopped)
		} else {
			m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusError)
			m.logChunk(env.ID, fmt.Sprintf("[error] failed to start: %v\n", startErr))
			slog.Error("environment start failed", "env_id", env.ID, "err", startErr)
		}
		return
	}

	m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusRunning)
	m.logChunk(env.ID, "[environment running — waiting for stop signal]\n")
	slog.Info("environment running", "env_id", env.ID)

	// Wait for stop signal.
	<-ctx.Done()

	// Tear down.
	m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusStopping)
	m.logChunk(env.ID, "\n[stopping environment...]\n")
	slog.Info("tearing down environment", "env_id", env.ID)
	m.teardown(env, repoPath)
	m.database.UpdateEnvironmentStatus(env.ID, db.EnvironmentStatusStopped)
	m.logChunk(env.ID, "[environment stopped]\n")
	slog.Info("environment stopped", "env_id", env.ID)
}

// startCompose runs docker compose up --build -d.
func (m *EnvironmentManager) startCompose(ctx context.Context, env *db.Environment, repoPath string) error {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", env.ConfigPath, "up", "--build", "-d")
	cmd.Dir = repoPath
	return m.runCmd(ctx, env.ID, cmd)
}

// startDockerfile builds an image then runs a detached container.
func (m *EnvironmentManager) startDockerfile(ctx context.Context, env *db.Environment, repoPath string) error {
	imgName := fmt.Sprintf("kodama-env-%d", env.ProjectID)
	containerName := fmt.Sprintf("kodama-env-%d", env.ProjectID)

	// Remove any existing container (best-effort).
	exec.Command("docker", "rm", "-f", containerName).Run() //nolint:errcheck

	// Build image.
	m.logChunk(env.ID, fmt.Sprintf("[building image %s...]\n", imgName))
	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", imgName, "-f", env.ConfigPath, repoPath)
	buildCmd.Dir = repoPath
	if err := m.runCmd(ctx, env.ID, buildCmd); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}

	// Run container (detached, repo mounted at /workspace).
	m.logChunk(env.ID, fmt.Sprintf("[running container %s...]\n", containerName))
	runCmd := exec.CommandContext(ctx,
		"docker", "run", "-d",
		"--name", containerName,
		"-v", repoPath+":/workspace",
		imgName,
		"tail", "-f", "/dev/null",
	)
	return m.runCmd(ctx, env.ID, runCmd)
}

// teardown stops the docker environment (best-effort, no context).
func (m *EnvironmentManager) teardown(env *db.Environment, repoPath string) {
	var cmds []*exec.Cmd
	switch env.Type {
	case "compose":
		cmds = []*exec.Cmd{
			exec.Command("docker", "compose", "-f", env.ConfigPath, "down"),
		}
		for _, c := range cmds {
			c.Dir = repoPath
		}
	case "dockerfile":
		containerName := fmt.Sprintf("kodama-env-%d", env.ProjectID)
		cmds = []*exec.Cmd{
			exec.Command("docker", "stop", containerName),
			exec.Command("docker", "rm", containerName),
		}
	}
	for _, cmd := range cmds {
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Warn("env teardown error", "env_id", env.ID, "cmd", cmd.Args, "err", err, "output", string(out))
		}
	}
}

// runCmd runs a command, streaming combined stdout+stderr to the env log.
func (m *EnvironmentManager) runCmd(ctx context.Context, envID int64, cmd *exec.Cmd) error {
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return err
	}
	// Close parent's write end so reading from r reaches EOF when child exits.
	w.Close()

	// Stream output to log.
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			m.logChunk(envID, string(buf[:n]))
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	return cmd.Wait()
}

// logChunk appends a chunk to the DB log and broadcasts it via WebSocket.
func (m *EnvironmentManager) logChunk(envID int64, chunk string) {
	if err := m.database.AppendEnvironmentLog(envID, chunk); err != nil {
		slog.Warn("append env log", "env_id", envID, "err", err)
	}
	if m.hub != nil {
		m.hub.Broadcast(envID, chunk)
	}
}

// injectEnvContext prepends a docker exec usage note to a task prompt.
func injectEnvContext(prompt string, env *db.Environment) string {
	var note string
	switch env.Type {
	case "compose":
		note = fmt.Sprintf(
			"Note: A dev environment is running for this project (repo mounted at /workspace).\n"+
				"Write and edit files normally on the host. To build or test, run commands inside the container:\n"+
				"  docker compose -f %s exec <service> <command>\n"+
				"  e.g. docker compose -f %s exec app go test ./...\n\n",
			env.ConfigPath, env.ConfigPath,
		)
	case "dockerfile":
		containerName := fmt.Sprintf("kodama-env-%d", env.ProjectID)
		note = fmt.Sprintf(
			"Note: A dev environment is running for this project (repo mounted at /workspace).\n"+
				"Write and edit files normally on the host. To build or test, run commands inside the container:\n"+
				"  docker exec %s <command>\n"+
				"  e.g. docker exec %s go test ./...\n\n",
			containerName, containerName,
		)
	default:
		return prompt
	}
	return note + prompt
}
