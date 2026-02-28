# Kodama

A self-hosted autonomous coding daemon that wraps Claude Code and Codex as subprocesses, managing async task execution via a web UI.

Named after the Japanese forest spirit that quietly works in the background.

## Why Kodama?

I built this to scratch my own itch: a self-hosted daemon that can run coding tasks asynchronously while I do other work. Kodama is the result of that.

## Quick Start

```bash
# Start daemon + web UI
kodama
```

Open http://localhost:8080 to access the web UI.
On first start, complete the setup wizard in the browser.

## Features

- **Web UI**: Project/backlog management, live task output streaming via WebSocket
- **Telegram**: Notifications when Claude Code has questions; reply to answer
- **Rate limit handling**: Detects rate limits, saves checkpoint, retries after 5h
- **YOLO failover**: Optional per-task switch (Claude→Codex) on rate limit
- **Docker environments**: Run agents inside project-specific containers
- **Multi-agent**: Per-task agent selection (Claude Code or Codex)

## Configuration

Kodama uses built-in defaults (port 8080, data dir `~/.kodama`) and reads overrides from environment variables.
Telegram configuration is managed in the web UI (Settings).

Environment variables:

```
KODAMA_PORT
KODAMA_DATA_DIR
KODAMA_QUESTION_TIMEOUT
KODAMA_CLAUDE_BINARY
KODAMA_DOCKER_SOCKET
```

## Project Config (`kodama.yml`)

Each managed project has a `kodama.yml` in its repo root:

```yaml
name: My Project
repo: github.com/user/myproject
image: golang:1.22      # Docker image (optional)
agent: codex            # default: codex
```

## Communication Protocol

Agents communicate with Kodama via structured prefixes in stdout:

| Prefix | Meaning |
|--------|---------|
| `KODAMA_QUESTION:` | Needs user input |
| `KODAMA_DONE:` | Task completed |
| `KODAMA_PR:` | PR URL follows |
| `KODAMA_DECISION:` | Architectural decision (updates kodama.md) |
| `KODAMA_BLOCKED:` | Cannot proceed |

All agents must emit the protocol lines for reliable status detection. Codex runs in full-auto mode, so any questions will be handled by stopping the run and resuming via injected context (no session resume).

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
│   ├── config/          # config loading (YAML + env)
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

```yaml
services:
  kodama:
    image: kodama:latest
    volumes:
      - ./data:/data
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - KODAMA_PORT=8080
    restart: unless-stopped
```

## License

MIT
