# Kodama

A self-hosted autonomous coding daemon that wraps Claude Code and Codex as subprocesses, managing async task execution via a web UI.

Named after the Japanese forest spirit that quietly works in the background.

## Quick Start

```bash
# Start daemon + web UI
kodama
```

Open http://localhost:8080 to access the web UI.

## Features

- **Web UI**: Project/backlog management, live task output streaming via WebSocket
- **Telegram**: Notifications when Claude Code has questions; reply to answer
- **Rate limit handling**: Detects rate limits, saves checkpoint, retries after 5h
- **YOLO failover**: Optionally switch agent (Claude→Codex) on rate limit
- **Docker environments**: Run agents inside project-specific containers
- **Multi-agent**: Per-task agent selection (Claude Code or Codex)

## Configuration

Kodama looks for config in `./kodama-server.yml` or `~/.config/kodama/config.yml`.

```yaml
port: 8080
data_dir: ./data
question_timeout: 30     # seconds before detecting agent is waiting

telegram:
  token: your-bot-token
  user_id: 12345678       # your Telegram user ID

docker:
  socket: /var/run/docker.sock

claude:
  binary: claude           # path to claude binary
```

All values can be overridden via environment variables:

```
KODAMA_PORT
KODAMA_DATA_DIR
KODAMA_QUESTION_TIMEOUT
KODAMA_TELEGRAM_TOKEN
KODAMA_TELEGRAM_USER_ID
KODAMA_CLAUDE_BINARY
KODAMA_DOCKER_SOCKET
```

## Project Config (`kodama.yml`)

Each managed project has a `kodama.yml` in its repo root:

```yaml
name: My Project
repo: github.com/user/myproject
image: golang:1.22      # Docker image (optional)
agent: claude           # default: claude
failover: false         # YOLO mode: switch agent on rate limit
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
      - KODAMA_TELEGRAM_TOKEN=xxx
      - KODAMA_TELEGRAM_USER_ID=yyy
      - KODAMA_PORT=8080
    restart: unless-stopped
```

## License

MIT
