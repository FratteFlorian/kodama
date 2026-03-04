package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const kodamaMdTemplate = `# %s

## Goals & Scope
%s

## Tech Stack & Conventions
_To be filled in as decisions are made._

## Task Profiles
Kodama can apply per-task execution profiles to shape behavior:

- architect: focus on design, trade-offs, interfaces, and migration strategy
- developer: implement changes end-to-end with focused diffs and tests
- qa: find defects/regressions and validate edge cases
- refactorer: improve structure without changing intended behavior
- incident: optimise for safe and fast mitigation + root cause clarity
- ux-reviewer: review from end-user perspective (flows, friction, states, copy)

## Architecture Decisions
_None yet._

## Current Status
Project initialized on %s. No tasks completed yet.

## Open Decisions / Known Issues
_None yet._

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
`

const claudeMdBootstrap = `Read kodama.md at the start of every session.
It contains the full project context and the communication protocol you must follow.
`

const kodamaYmlTemplate = `name: %s
repo: %s
image: %s
agent: %s
telegram:
  notify: true
`

// GenerateKodamaMd generates the content of kodama.md from a project name and PRD.
func GenerateKodamaMd(name, prd string) string {
	return fmt.Sprintf(kodamaMdTemplate, name, prd, time.Now().Format("2006-01-02"))
}

// InitProject creates kodama.md, CLAUDE.md, and kodama.yml in the project repo.
func InitProject(repoPath, name, prd, image, agent string) error {
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}

	// Write kodama.md (only if it doesn't exist — preserve existing project context)
	kodamaMdPath := filepath.Join(repoPath, "kodama.md")
	if _, err := os.Stat(kodamaMdPath); os.IsNotExist(err) {
		kodamaMd := GenerateKodamaMd(name, prd)
		if err := os.WriteFile(kodamaMdPath, []byte(kodamaMd), 0644); err != nil {
			return fmt.Errorf("write kodama.md: %w", err)
		}
	}

	// Write CLAUDE.md bootstrap (only if it doesn't exist)
	claudeMdPath := filepath.Join(repoPath, "CLAUDE.md")
	if _, err := os.Stat(claudeMdPath); os.IsNotExist(err) {
		if err := os.WriteFile(claudeMdPath, []byte(claudeMdBootstrap), 0644); err != nil {
			return fmt.Errorf("write CLAUDE.md: %w", err)
		}
	}

	// Write kodama.yml (only if it doesn't exist — preserve existing config)
	kodamaYmlPath := filepath.Join(repoPath, "kodama.yml")
	if _, err := os.Stat(kodamaYmlPath); os.IsNotExist(err) {
		repoURL := "github.com/user/" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		kodamaYml := fmt.Sprintf(kodamaYmlTemplate, name, repoURL, image, agent)
		if err := os.WriteFile(kodamaYmlPath, []byte(kodamaYml), 0644); err != nil {
			return fmt.Errorf("write kodama.yml: %w", err)
		}
	}

	return nil
}
