package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UpdateKodamaMd appends decisions and updates current status in kodama.md.
func UpdateKodamaMd(repoPath string, decisions []string, doneSummary string) error {
	kodamaMdPath := filepath.Join(repoPath, "kodama.md")
	data, err := os.ReadFile(kodamaMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no kodama.md to update
		}
		return fmt.Errorf("read kodama.md: %w", err)
	}

	content := string(data)

	// Append decisions to Architecture Decisions section.
	if len(decisions) > 0 {
		decisionBlock := "\n"
		for _, d := range decisions {
			decisionBlock += fmt.Sprintf("- %s (%s)\n", d, time.Now().Format("2006-01-02"))
		}
		content = insertIntoSection(content, "## Architecture Decisions", decisionBlock)
	}

	// Update Current Status section.
	if doneSummary != "" {
		statusEntry := fmt.Sprintf("\n- %s — %s\n", time.Now().Format("2006-01-02"), doneSummary)
		content = insertIntoSection(content, "## Current Status", statusEntry)
	}

	return os.WriteFile(kodamaMdPath, []byte(content), 0644)
}

// insertIntoSection finds a markdown section heading and appends text after the first line of content.
func insertIntoSection(content, heading, text string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inSection := false
	inserted := false

	for i, line := range lines {
		result = append(result, line)

		if strings.TrimSpace(line) == strings.TrimSpace(heading) {
			inSection = true
			continue
		}

		if inSection && !inserted {
			// Insert after heading.
			// Find the first content line (skip empty lines right after heading).
			if i > 0 && strings.TrimSpace(lines[i-1]) == strings.TrimSpace(heading) {
				// We just appended the heading, now append text.
				result = append(result, text)
				inserted = true
				inSection = false
			}
		}
	}

	if !inserted && inSection {
		result = append(result, text)
	}

	return strings.Join(result, "\n")
}

// ExtractDecisions extracts KODAMA_DECISION: payloads from task output.
func ExtractDecisions(output string) []string {
	var decisions []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "KODAMA_DECISION:") {
			payload := strings.TrimSpace(trimmed[len("KODAMA_DECISION:"):])
			if payload != "" {
				decisions = append(decisions, payload)
			}
		}
	}
	return decisions
}

// ExtractDoneSummary extracts the KODAMA_DONE: payload from task output.
func ExtractDoneSummary(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "KODAMA_DONE:") {
			return strings.TrimSpace(trimmed[len("KODAMA_DONE:"):])
		}
	}
	return ""
}

// ExtractChecklist extracts markdown checklist items from output (for checkpoints).
func ExtractChecklist(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]") {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}
