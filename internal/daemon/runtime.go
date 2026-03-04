package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/florian/kodama/internal/db"
)

const (
	runtimeModeHost   = "host"
	runtimeModeDocker = "docker"
)

type dockerStack string

const (
	dockerStackGo      dockerStack = "go"
	dockerStackNode    dockerStack = "node"
	dockerStackPython  dockerStack = "python"
	dockerStackRust    dockerStack = "rust"
	dockerStackLaravel dockerStack = "laravel"
	dockerStackPHP     dockerStack = "php"
	dockerStackSpring  dockerStack = "spring"
	dockerStackGeneric dockerStack = "generic"
)

func normalizeRuntimeMode(raw string) string {
	switch raw {
	case runtimeModeDocker:
		return runtimeModeDocker
	default:
		return runtimeModeHost
	}
}

func (d *Daemon) ensureProjectRuntime(ctx context.Context, proj *db.Project) error {
	if normalizeRuntimeMode(proj.RuntimeMode) != runtimeModeDocker {
		return nil
	}
	if proj.RepoPath == "" {
		return fmt.Errorf("docker runtime requires a repository path")
	}

	configPath, err := ensureDockerScaffold(proj.RepoPath)
	if err != nil {
		return err
	}

	env, err := d.db.GetEnvironment(proj.ID)
	if err != nil {
		return fmt.Errorf("get environment: %w", err)
	}
	if env == nil || env.Type != "compose" || strings.TrimSpace(env.ConfigPath) == "" {
		env, err = d.db.UpsertEnvironment(proj.ID, "compose", configPath)
		if err != nil {
			return fmt.Errorf("create environment: %w", err)
		}
	}

	if !d.envManager.IsRunning(env.ID) {
		if err := d.envManager.Start(ctx, env, proj.RepoPath); err != nil {
			return fmt.Errorf("start docker runtime: %w", err)
		}
	}
	if err := d.waitForEnvironmentRunning(ctx, proj.ID, 2*time.Minute); err != nil {
		return err
	}
	return nil
}

func (d *Daemon) waitForEnvironmentRunning(ctx context.Context, projectID int64, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		env, err := d.db.GetEnvironment(projectID)
		if err != nil {
			return fmt.Errorf("get environment while waiting: %w", err)
		}
		if env == nil {
			return fmt.Errorf("environment not found")
		}
		if env.Status == db.EnvironmentStatusRunning && d.envManager.IsRunning(env.ID) {
			return nil
		}
		if env.Status == db.EnvironmentStatusError {
			return fmt.Errorf("environment failed to start")
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("environment startup timeout")
		case <-ticker.C:
		}
	}
}

func (d *Daemon) stopProjectRuntime(projectID int64) {
	proj, err := d.db.GetProject(projectID)
	if err != nil || proj == nil {
		return
	}
	if normalizeRuntimeMode(proj.RuntimeMode) != runtimeModeDocker {
		return
	}

	env, err := d.db.GetEnvironment(projectID)
	if err != nil || env == nil {
		return
	}
	if !d.envManager.IsRunning(env.ID) {
		return
	}
	d.envManager.StopAndWait(env.ID)
}

func ensureDockerScaffold(repoPath string) (string, error) {
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return "", fmt.Errorf("ensure repo path: %w", err)
	}

	dockerfilePath := filepath.Join(repoPath, "Dockerfile")
	composePath := filepath.Join(repoPath, "docker-compose.yml")
	dockerIgnorePath := filepath.Join(repoPath, ".dockerignore")

	stack := detectDockerStack(repoPath)

	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		if err := os.WriteFile(dockerfilePath, []byte(renderDockerfile(stack)), 0644); err != nil {
			return "", fmt.Errorf("write Dockerfile: %w", err)
		}
	}

	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		if err := os.WriteFile(composePath, []byte(renderComposeFile()), 0644); err != nil {
			return "", fmt.Errorf("write docker-compose.yml: %w", err)
		}
	}

	if _, err := os.Stat(dockerIgnorePath); os.IsNotExist(err) {
		if err := os.WriteFile(dockerIgnorePath, []byte(renderDockerIgnore()), 0644); err != nil {
			return "", fmt.Errorf("write .dockerignore: %w", err)
		}
	}

	return "docker-compose.yml", nil
}

func detectDockerStack(repoPath string) dockerStack {
	if fileExists(filepath.Join(repoPath, "composer.json")) && fileExists(filepath.Join(repoPath, "artisan")) {
		return dockerStackLaravel
	}
	if fileExists(filepath.Join(repoPath, "composer.json")) || fileExists(filepath.Join(repoPath, "index.php")) {
		return dockerStackPHP
	}
	if fileExists(filepath.Join(repoPath, "pom.xml")) ||
		fileExists(filepath.Join(repoPath, "build.gradle")) ||
		fileExists(filepath.Join(repoPath, "build.gradle.kts")) {
		return dockerStackSpring
	}
	if fileExists(filepath.Join(repoPath, "go.mod")) {
		return dockerStackGo
	}
	if fileExists(filepath.Join(repoPath, "package.json")) {
		return dockerStackNode
	}
	if fileExists(filepath.Join(repoPath, "pyproject.toml")) || fileExists(filepath.Join(repoPath, "requirements.txt")) {
		return dockerStackPython
	}
	if fileExists(filepath.Join(repoPath, "Cargo.toml")) {
		return dockerStackRust
	}
	return dockerStackGeneric
}

func renderDockerfile(stack dockerStack) string {
	switch stack {
	case dockerStackLaravel:
		return `FROM php:8.3-cli-bookworm

WORKDIR /workspace

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    unzip \
    curl \
    libzip-dev \
    libpq-dev \
    libonig-dev && \
    docker-php-ext-install pdo pdo_mysql pdo_pgsql mbstring zip bcmath && \
    rm -rf /var/lib/apt/lists/*

COPY --from=composer:2 /usr/bin/composer /usr/bin/composer

COPY composer.json composer.lock* ./
RUN if [ -f composer.json ]; then composer install --no-interaction --prefer-dist; fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackPHP:
		return `FROM php:8.3-cli-bookworm

WORKDIR /workspace

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    unzip \
    curl \
    libzip-dev \
    libpq-dev \
    libonig-dev && \
    docker-php-ext-install pdo pdo_mysql pdo_pgsql mbstring zip bcmath && \
    rm -rf /var/lib/apt/lists/*

COPY --from=composer:2 /usr/bin/composer /usr/bin/composer

COPY composer.json composer.lock* ./
RUN if [ -f composer.json ]; then composer install --no-interaction --prefer-dist; fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackSpring:
		return `FROM maven:3.9-eclipse-temurin-21

WORKDIR /workspace

COPY pom.xml ./
COPY mvnw* ./
COPY .mvn ./.mvn
RUN if [ -f pom.xml ]; then mvn -q -DskipTests dependency:go-offline || true; fi

COPY build.gradle* settings.gradle* gradlew* ./
COPY gradle ./gradle
RUN if [ -f gradlew ]; then chmod +x gradlew && ./gradlew --no-daemon dependencies || true; fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackGo:
		return `FROM golang:1.22-bookworm

WORKDIR /workspace

ENV CGO_ENABLED=0

COPY go.mod go.sum* ./
RUN if [ -f go.mod ]; then go mod download; fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackNode:
		return `FROM node:22-bookworm

WORKDIR /workspace

COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* .npmrc* ./
RUN if [ -f package-lock.json ]; then npm ci; \
    elif [ -f pnpm-lock.yaml ]; then corepack enable && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then corepack enable && yarn install --frozen-lockfile; \
    elif [ -f package.json ]; then npm install; \
    fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackPython:
		return `FROM python:3.12-slim

WORKDIR /workspace

ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1
ENV PIP_DISABLE_PIP_VERSION_CHECK=1

COPY requirements*.txt ./
COPY pyproject.toml* poetry.lock* ./
RUN pip install --upgrade pip && \
    if [ -f requirements.txt ]; then pip install -r requirements.txt; fi && \
    if [ -f pyproject.toml ]; then pip install -e . || true; fi

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	case dockerStackRust:
		return `FROM rust:1.78-bookworm

WORKDIR /workspace

COPY Cargo.toml Cargo.lock* ./
RUN mkdir -p src && printf 'fn main() {}\n' > src/main.rs && cargo fetch || true
RUN rm -rf src

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	default:
		return `FROM ubuntu:24.04

WORKDIR /workspace

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    git \
    make && \
    rm -rf /var/lib/apt/lists/*

COPY . .

CMD ["tail", "-f", "/dev/null"]
`
	}
}

func renderComposeFile() string {
	return `services:
  app:
    build:
      context: .
      dockerfile: Dockerfile
    working_dir: /workspace
    volumes:
      - ./:/workspace
    stdin_open: true
    tty: true
    command: tail -f /dev/null
`
}

func renderDockerIgnore() string {
	return `.git
.gitignore
.github
.kodama
.idea
.vscode
.env
.env.*

node_modules
.npm
.pnpm-store
.yarn
vendor

.venv
venv
__pycache__
.pytest_cache
.mypy_cache

bin
dist
build
coverage
target
*.log
`
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
