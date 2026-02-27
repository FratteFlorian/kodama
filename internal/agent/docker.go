package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sync"
)

// DockerAgent wraps another agent and runs it inside a Docker container.
type DockerAgent struct {
	inner    Agent
	image    string
	repoPath string

	cmd    *exec.Cmd
	cancel context.CancelFunc
	output chan string
	mu     sync.Mutex
	stdin2 interface{ Write([]byte) (int, error) }
}

// NewDockerAgent wraps an inner agent to run inside a Docker container.
// If image is empty, it returns the inner agent directly.
func NewDockerAgent(inner Agent, image, repoPath string) Agent {
	if image == "" {
		return inner
	}
	return &DockerAgent{
		inner:    inner,
		image:    image,
		repoPath: repoPath,
		output:   make(chan string, 256),
	}
}

// Start runs the agent inside a Docker container.
func (a *DockerAgent) Start(workdir, task, contextFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Build docker run command that runs the inner agent's binary.
	// We use the same pattern but wrap with docker run.
	mountPath := a.repoPath
	if mountPath == "" {
		mountPath = workdir
	}

	prompt := task
	if contextFile != "" {
		prompt = fmt.Sprintf("Please read %s for project context first, then: %s", contextFile, task)
	}

	args := []string{
		"run", "--rm",
		"-v", mountPath + ":/workspace",
		"-w", "/workspace",
		a.image,
		"claude", "--print", "--dangerously-skip-permissions", prompt,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	a.cmd = cmd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	a.stdin2 = stdinPipe

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start docker: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			a.output <- scanner.Text() + "\n"
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.output <- scanner.Text() + "\n"
		}
	}()

	go func() {
		wg.Wait()
		cmd.Wait()
		close(a.output)
	}()

	return nil
}

// Write sends input to the container's stdin.
func (a *DockerAgent) Write(input string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin2 == nil {
		return fmt.Errorf("agent not started")
	}
	_, err := a.stdin2.Write([]byte(input + "\n"))
	return err
}

// Output returns the output channel.
func (a *DockerAgent) Output() <-chan string {
	return a.output
}

// Detect delegates to ParseSignal.
func (a *DockerAgent) Detect(line string) (Signal, string) {
	return ParseSignal(line)
}

// Stop kills the Docker container.
func (a *DockerAgent) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
