# Kodama Code

Kodama is a self-hosted autonomous coding daemon that wraps Claude Code, allowing async task execution via a web UI. It is a personal developer productivity tool — not a general-purpose product — but built cleanly enough to be shared as open source.

The name comes from the Japanese forest spirit that quietly works in the background. That's exactly what Kodama does.

## Core Philosophy

- **Minimal CLI surface** — one command only, everything else lives in the UI
- **Single binary** — Go, fully self-contained, embeds frontend assets
- **Always-on daemon** — web UI starts automatically with the daemon
- **Tests are not optional** — every component must have tests, no exceptions
- **Fits within Anthropic ToS** — Kodama wraps the official `claude` CLI as a subprocess, never extracts or reuses OAuth tokens

## Commands

```
kodama          # Start daemon + web UI (default port 8080)
```

No other CLI commands. Project init and backlog management happen inside the UI.

## Architecture

### Daemon
- Always-on background process
- Manages a SQLite-backed task queue (backlog)
- Processes backlog items sequentially, one at a time
- Spawns Claude Code as a subprocess per task with stdin/stdout pipes
- Streams Claude Code output to web UI via WebSockets in real time
- Detects when Claude Code is waiting for input (pattern match + output timeout)
- On question detected: sends Telegram notification, waits for reply, writes reply to Claude Code stdin
- Handles rate limit errors: persists task state (last checklist), queues for retry after 5 hours
- On resume: prepends last checklist state to task prompt so Claude Code continues naturally
- After task completion: updates project CLAUDE.md with decisions made, opens PR

### Web UI
- Served automatically by the daemon
- Built with Go `html/template` + HTMX (no React, no build step)
- Frontend assets embedded in binary via `go:embed`
- Features:
  - Project list + creation form (generates `kodama.yml` + project CLAUDE.md)
  - Backlog management per project (add/reorder/remove items)
  - Live task output streaming via WebSocket
  - Task history with output logs
  - "Start" button per project — kicks off sequential backlog processing
  - Status indicators: idle / running / waiting for input / rate limited / done

### Telegram Bot
- Uses go-telegram-bot-api
- **Whitelisted to a single Telegram user ID** — all other messages silently ignored
- Sends notifications when Claude Code has a question, including the question text
- Accepts reply and forwards it to the waiting Claude Code stdin
- Also notifies on: task completed (+ PR link), rate limit hit, rate limit reset + resuming

### Project Isolation (Dev Environments)
- Each project defines its runtime environment in `kodama.yml`
- Kodama runs Claude Code inside a Docker container matching the project's environment
- Ensures correct toolchain per project (Go, Java, Node, Python, etc.)
- Container mounts the project repo directory

### SQLite Schema (rough)
- `projects` — id, name, repo_url, docker_image, created_at
- `tasks` — id, project_id, description, status, created_at, started_at, completed_at
- `task_logs` — id, task_id, output (streamed chunks), timestamp
- `task_checkpoints` — id, task_id, checklist_state (for rate limit resume)

## Project Config (`kodama.yml`)

Generated during project creation in the UI. Lives in the project repo root.

```yaml
name: My Project
repo: github.com/user/myproject
image: golang:1.22        # Docker image for dev environment
telegram:
  notify: true
```

## Per-Project CLAUDE.md

Each project has its own `CLAUDE.md` in the repo root. Kodama:
1. Generates an initial `CLAUDE.md` from the PRD/description provided at project creation
2. Automatically updates it after each completed task with decisions made

The `CLAUDE.md` is the persistent memory layer across sessions. It should include:
- Project goals & scope
- Tech stack & conventions
- Architecture decisions
- Current status / what has been done
- Open decisions / known issues

## Rate Limit Handling

Claude Code Pro/Max plans have a 5-hour rolling token limit. Kodama handles this gracefully:

1. Detects rate limit signal from Claude Code output
2. Captures last checklist state from output
3. Marks task as `rate_limited` with checkpoint saved
4. Schedules retry after 5 hours
5. Sends Telegram notification: "Rate limit hit, will resume in 5h"
6. On retry: prepends checkpoint context, resumes task naturally
7. Sends Telegram notification: "Resuming task: [name]"

## Question Detection

When Claude Code stops and waits for input, Kodama detects this via:
- Output timeout (no new output for N seconds, configurable)
- Pattern matching on known question indicators in Claude Code output

On detection:
- Web UI shows "Waiting for input" status with the question
- Telegram notification sent with question text
- Daemon holds stdin pipe open, waiting
- User replies via Telegram → daemon writes reply to stdin → Claude Code continues

## Agent Abstraction

Kodama supports multiple coding agents through a common interface. This allows per-task agent selection and automatic failover.

### Agent Interface

```go
type Agent interface {
    Start(task string, contextFile string) error
    Write(input string) error      // feed stdin / answer question
    Output() <-chan string          // stream output
    Detect(output string) Signal   // parse KODAMA_* prefixes
    Stop() error
}
```

Implementations:
- `ClaudeAgent` — wraps the `claude` CLI
- `CodexAgent` — wraps `codex exec --full-auto --json`

### Per-Task Agent Selection

Each task in the backlog has an agent selector, defaulting to the project-level agent in `kodama.yml`:

```yaml
name: My Project
agent: codex           # project-level default
image: golang:1.22
```

In the web UI, each task shows an agent dropdown:
```
[ ] Implement auth module          [agent: claude  ▼]
[ ] Write tests for auth module    [agent: codex   ▼]
[ ] Review PR #42                  [agent: codex   ▼]
[ ] Refactor database layer        [agent: claude  ▼]
```

Suggested defaults by task type:
- **Implementation, refactoring, architecture** → Claude Code
- **Code review, test generation, release notes, changelog** → Codex

### YOLO Failover

When a task has failover enabled in the UI:

1. Primary agent hits rate limit mid-task
2. Kodama switches to the other agent immediately (no waiting)
3. New agent is instructed to read `kodama.md` for full project context
4. Checklist state from the previous agent is prepended as resume context
5. Execution continues — the human reviews the PR and catches any inconsistencies

Failover is opt-in per task. When disabled, Kodama waits for the rate limit reset as usual.

Failover never switches mid-task by default — but with failover enabled (YOLO mode) it does. This is intentional: the user owns the risk and reviews the PR anyway.

## kodama.md — Project Memory File

Every project managed by Kodama has a `kodama.md` in the repo root. This is the **single source of truth** for project context, agent-agnostic and future-proof.

**Why `kodama.md` and not `CLAUDE.md`:**
- Agent-agnostic by name — not tied to any specific agent's branding
- Both Claude Code and Codex are instructed to read it at session start
- Contains the Kodama communication protocol which applies equally to all agents
- Future-proof — any new agent just reads the same file

### Bootstrap Files

Each agent has its own bootstrap config that simply instructs it to read `kodama.md`:

**`CLAUDE.md`** (for Claude Code):
```
Read kodama.md at the start of every session.
It contains the full project context and the communication protocol you must follow.
```

**Codex** (`~/.codex/config.toml` or per-project config):
```
Read kodama.md at the start of every session.
It contains the full project context and the communication protocol you must follow.
```

### kodama.md Structure

Generated during project creation from the PRD. Automatically updated after each completed task.

```markdown
# Project Name

## Goals & Scope
...

## Tech Stack & Conventions
...

## Architecture Decisions
...

## Current Status
...

## Open Decisions / Known Issues
...

## Communication Protocol
When working on a task managed by Kodama, always use these prefixes:

| Prefix | Meaning |
|---|---|
| KODAMA_QUESTION: | Needs user input |
| KODAMA_DONE: | Task completed, summary follows |
| KODAMA_PR: | PR URL follows |
| KODAMA_DECISION: | Architectural decision made, will update kodama.md |
| KODAMA_BLOCKED: | Cannot proceed, reason follows |

Never stop and wait without using one of these prefixes.
```

## Tech Stack

- **Language:** Go 1.22+
- **Web framework:** standard `net/http` + Chi router
- **Frontend:** HTMX + Go templates, embedded via `go:embed`
- **Database:** SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- **Telegram:** `github.com/go-telegram-bot-api/telegram-bot-api/v5`
- **Process management:** `os/exec` with stdin/stdout pipes

## Testing

**Tests are not optional.** Every component must be tested.

- Unit tests for all core logic (task queue, rate limit detection, question detection, checklist parsing)
- Integration tests for Claude Code subprocess management (use a mock `claude` binary)
- Integration tests for Telegram bot (mock the Telegram API)
- API tests for all web UI endpoints
- Use Go's standard `testing` package
- Use `testify` for assertions
- Aim for >80% coverage on core packages
- Tests must pass before any PR is merged
- CI via GitHub Actions

## Deployment

Runs as a container on Proxmox. Accessible via:
- **Tailscale** for private access
- **Cloudflare Tunnel** for access without Tailscale client

Docker Compose example:
```yaml
services:
  kodama:
    image: kodama:latest
    volumes:
      - ./data:/data           # SQLite + project state
      - /var/run/docker.sock:/var/run/docker.sock  # for spawning project containers
    environment:
      - KODAMA_TELEGRAM_TOKEN=xxx
      - KODAMA_TELEGRAM_USER_ID=yyy
      - KODAMA_PORT=8080
    restart: unless-stopped
```

## Repository Structure

```
kodama/
├── cmd/
│   └── kodama/
│       └── main.go
├── internal/
│   ├── daemon/          # core daemon loop, task queue processing
│   ├── claude/          # Claude Code subprocess management, streaming
│   ├── db/              # SQLite models and queries
│   ├── telegram/        # Telegram bot integration
│   ├── web/             # HTTP server, handlers, WebSocket streaming
│   │   └── templates/   # Go HTML templates
│   │   └── static/      # JS/CSS (HTMX etc), embedded
├── tests/
│   └── mocks/           # mock claude binary, mock telegram API
├── kodama.yml           # Kodama's own project config (dogfooding)
├── CLAUDE.md            # This file
└── README.md
```

## Configuration

Kodama supports both a config file and environment variables. **Environment variables take precedence over the config file.**

### Config File

Kodama looks for a config file in the following order (first found wins):
1. `./kodama-server.yml` — local directory (useful for development)
2. `~/.config/kodama/config.yml` — user config directory

All values are optional and fall back to defaults if not set. Secrets (Telegram token etc.) can safely be stored in the config file for personal/homelab use — the file is never part of any project repo.

```yaml
port: 8080
data_dir: ./data
question_timeout: 30        # seconds before detecting CC is waiting

telegram:
  token: xxx
  user_id: yyy              # whitelisted Telegram user ID

docker:
  socket: /var/run/docker.sock

# optional: override claude binary path
claude:
  binary: claude            # default: looks for claude in $PATH
```

### Environment Variables

Environment variables override config file values. Useful for container deployments or CI.

```
KODAMA_PORT              # Web UI port (default: 8080)
KODAMA_DATA_DIR          # SQLite + state directory (default: ./data)
KODAMA_TELEGRAM_TOKEN    # Telegram bot token
KODAMA_TELEGRAM_USER_ID  # Whitelisted Telegram user ID (only user who can interact)
KODAMA_QUESTION_TIMEOUT  # Seconds before detecting CC is waiting (default: 30)
KODAMA_CLAUDE_BINARY     # Path to claude binary (default: claude)
KODAMA_DOCKER_SOCKET     # Docker socket path (default: /var/run/docker.sock)
```

### Precedence

```
Environment variables > config file > defaults
```

### Setup Script

A `setup.sh` script should be provided for clean OS installations. It should:
1. Install Docker
2. Install and authenticate the `claude` CLI
3. Install the `kodama` binary
4. Interactively generate `~/.config/kodama/config.yml`
5. Set up Cloudflare Tunnel or Tailscale
6. Create and enable a systemd service for auto-start
7. Verify everything is running

Goal: fully running Kodama from a clean OS with a single script.

## Communication Protocol

Kodama detects structured signals from agent output via simple string prefix matching — no fragile heuristics or timeouts needed.

The full protocol definition lives in every project's `kodama.md` and is automatically included during project init. See the **kodama.md** section below for the full protocol definition.

For Kodama's own development, the active agent must follow this protocol at all times:

| Prefix | Meaning | Example |
|---|---|---|
| `KODAMA_QUESTION:` | Needs user input to proceed | `KODAMA_QUESTION: Should I use PostgreSQL or SQLite?` |
| `KODAMA_DONE:` | Task completed, summary follows | `KODAMA_DONE: Implemented auth module, all tests pass` |
| `KODAMA_PR:` | PR URL | `KODAMA_PR: https://github.com/user/repo/pull/42` |
| `KODAMA_DECISION:` | Architectural decision made, will update kodama.md | `KODAMA_DECISION: Using Chi router for HTTP layer` |
| `KODAMA_BLOCKED:` | Cannot proceed, reason follows | `KODAMA_BLOCKED: Missing environment variable DATABASE_URL` |

## What Kodama is NOT

- Not a replacement for Claude Code — it wraps it
- Not a multi-user tool — single user, personal use
- Not a general AI agent framework
- Not extracting or reusing Anthropic OAuth tokens in any way
- Not trying to bypass any rate limits — it respects them and waits

## Implementation Order

1. Core subprocess management (`internal/claude`) — spawn CC, pipe stdin/stdout, detect questions and rate limits
2. SQLite backlog (`internal/db`) — projects, tasks, logs, checkpoints
3. Daemon loop (`internal/daemon`) — sequential task processing, rate limit handling, resume logic
4. Web UI (`internal/web`) — project/task management, live WebSocket streaming
5. Telegram integration (`internal/telegram`) — notifications + reply forwarding
6. Docker project environments — per-project container spawning
8. Project init flow — kodama.yml + CLAUDE.md generation from PRD
9. Auto CLAUDE.md updates after task completion
