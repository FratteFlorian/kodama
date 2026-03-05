# Kodama

> [!WARNING]
> **Work in Progress (WIP):** Kodama is under active development. APIs, behavior, and data models may change without notice.
> Not recommended for production use yet.

A self-hosted autonomous coding daemon that wraps Claude Code and Codex as subprocesses, managing async task execution via a web UI.

Named after the Japanese forest spirit that quietly works in the background.

## Security Warning

Kodama has no built-in authentication. It is designed to run on localhost, a trusted local network, or behind a secure tunnel (for example Tailscale/VPN).

If you expose the Web UI to the public internet without access controls, anyone with access could trigger tasks that execute code via the integrated agents.

## Why Kodama?

I built this to scratch my own itch: a self-hosted daemon that can run coding tasks asynchronously while I do other work. Kodama is the result of that.

## Installation

```bash
# From source
git clone https://github.com/florian/kodama.git
cd kodama
make build
```

Binary output: `./kodama`

## Quick Start

```bash
# Start daemon + web UI
./kodama
```

Open http://localhost:8080 to access the web UI.
On first start, complete the setup page in the browser (Telegram can be left empty).

## Prerequisites

- `codex` CLI installed and authenticated (default agent; typically via subscription)
- `claude` CLI installed and authenticated (optional for Claude-based tasks; typically via subscription)
- Docker (optional, required for projects using `docker` command runtime)

Cost note:

- Kodama wraps official CLIs as subprocesses.
- In most setups it uses your existing CLI subscription/billing model.
- No direct Kodama API usage costs apply unless your CLI provider charges per call.

## Runtime Model

- Agents (`codex` / `claude`) always run on the host.
- Project commands (build/test/lint/etc.) run either on `host` or `docker`, per project.
- In `docker` mode, Kodama can scaffold missing `Dockerfile`/`docker-compose.yml` for the managed project repository.

## Features

- **Web UI**: Project/backlog management, live task output streaming via WebSocket
- **Telegram**: Notifications when Claude Code has questions; reply to answer
- **Rate limit handling**: Handles `KODAMA_RATELIMIT` signals, saves checkpoint, retries after 5h
- **YOLO failover**: Optional per-task switch (Claude→Codex) on rate limit
- **Command runtime mode**: Keep agents on host, run build/test commands on host or in auto-managed Docker
- **Multi-agent**: Per-task agent selection (Claude Code or Codex)
- **Parallel projects**: Multiple projects can run concurrently; each project executes its own backlog sequentially
- **Task profiles**: Per-task execution profile (Architect, Developer, QA, Refactorer, Incident, UX Reviewer)
- **Input attachments**: Attach PDFs/images/files to project PRDs and tasks
- **PRD task planning**: Generate backlog tasks from PRD context and auto-import structured plans
- **Auto Docker scaffold**: Generates `Dockerfile` + `docker-compose.yml` when Docker runtime is enabled and files are missing

## Configuration

Kodama uses built-in defaults (port 8080, data dir `~/.kodama`) and reads overrides from environment variables.
Telegram configuration is managed in the web UI (Settings).

Environment variables:

```
KODAMA_PORT
KODAMA_DATA_DIR
KODAMA_LOG
KODAMA_QUESTION_TIMEOUT
KODAMA_WAITING_REMINDER
KODAMA_CLAUDE_BINARY
KODAMA_DOCKER_SOCKET
```

Notes:

- `KODAMA_LOG`: set to `INFO` to reduce log verbosity (default is `DEBUG`).
- `KODAMA_QUESTION_TIMEOUT` and `KODAMA_WAITING_REMINDER` are seconds.
- Set `KODAMA_WAITING_REMINDER=0` to disable waiting reminders.

## Project Bootstrap (`kodama.yml`)

When creating a project with a repository path, Kodama writes a starter `kodama.yml` in the repo root (if missing):

```yaml
name: My Project
repo: github.com/user/myproject
image: ""
agent: codex
telegram:
  notify: true
```

`kodama.yml` is currently bootstrap metadata; active project settings are stored in Kodama's database and managed in the web UI.

## Communication Protocol

Agents communicate with Kodama via structured prefixes in stdout:

| Prefix | Meaning |
|--------|---------|
| `KODAMA_QUESTION:` | Needs user input |
| `KODAMA_DONE:` | Task completed |
| `KODAMA_RATELIMIT:` | Agent hit a rate limit |
| `KODAMA_PR:` | PR URL follows |
| `KODAMA_DECISION:` | Architectural decision (updates kodama.md) |
| `KODAMA_BLOCKED:` | Cannot proceed |

All agents must emit the protocol lines for reliable status detection. Codex runs in full-auto mode and now supports session resume via `codex exec resume` when Kodama captures a session ID.

## Telegram Commands

```
/projects
/tasks <project_id>
/task <project_id> <description>
/work <project_id>
/answer <task_id> <answer>
/help
```

## Telegram Setup

1. Create a bot with @BotFather and copy the token.
2. Get your user ID by messaging @userinfobot.
3. Start a chat with your bot and send `/start` once.
4. Open Kodama → Settings and enter the token + user ID.
5. Run a task to verify notifications.

## Security Notes

- Kodama is meant to run on a trusted network.
- If you expose it, use Cloudflare Access or a VPN like Tailscale.
- The UI has no built-in auth by design.

## Who Is This For?

- Solo developers who want a self-hosted coding daemon.
- People running a personal stack (homelab, VPS, or local machine).

## Known Limitations

- Single-user, self-hosted workflow.
- No built-in auth (use network-level controls).

## Contributing

- Issues and PRs are welcome.
- Keep changes focused and include tests for core logic.

## Architecture

```
kodama/
├── cmd/kodama/          # entrypoint
├── internal/
│   ├── config/          # config loading (defaults + env)
│   ├── db/              # SQLite schema and queries
│   ├── agent/           # Claude/Codex subprocess management
│   ├── daemon/          # task queue processing, rate limits
│   ├── telegram/        # bot notifications + question answering
│   ├── web/             # HTTP server, WebSocket, HTML templates
└── tests/mocks/         # mock agent binaries for testing
```

## Development

```bash
make build          # build binary
make test           # run all tests
make mock-binaries  # build mock claude/codex binaries
make lint           # run golangci-lint
```

## Deployment

For now, run the compiled binary directly (for example via `systemd`, `tmux`, or a process manager).

Recommended for public exposure:

- put Kodama behind authentication at the network edge (Cloudflare Access, Tailscale, VPN, reverse-proxy auth)
- do not expose it directly without access control

## License

MIT
