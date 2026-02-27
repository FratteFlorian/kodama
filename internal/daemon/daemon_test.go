package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateKodamaMd(t *testing.T) {
	content := GenerateKodamaMd("My Project", "Build a REST API for task management.")
	assert.Contains(t, content, "# My Project")
	assert.Contains(t, content, "Build a REST API for task management.")
	assert.Contains(t, content, "KODAMA_QUESTION:")
	assert.Contains(t, content, "KODAMA_DONE:")
	assert.Contains(t, content, "## Communication Protocol")
}

func TestInitProject(t *testing.T) {
	dir := t.TempDir()
	err := InitProject(dir, "Test Project", "A test project PRD", "golang:1.22", "claude", false)
	require.NoError(t, err)

	// Check kodama.md was created.
	kodamaMd, err := os.ReadFile(filepath.Join(dir, "kodama.md"))
	require.NoError(t, err)
	assert.Contains(t, string(kodamaMd), "# Test Project")

	// Check CLAUDE.md was created.
	claudeMd, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeMd), "kodama.md")

	// Check kodama.yml was created.
	kodamaYml, err := os.ReadFile(filepath.Join(dir, "kodama.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(kodamaYml), "name: Test Project")
	assert.Contains(t, string(kodamaYml), "golang:1.22")
}

func TestInitProjectDoesNotOverwriteClaudeMd(t *testing.T) {
	dir := t.TempDir()
	existing := "# My existing CLAUDE.md\nCustom content here."
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644))

	err := InitProject(dir, "Test", "PRD", "", "claude", false)
	require.NoError(t, err)

	// CLAUDE.md should be unchanged.
	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	assert.Equal(t, existing, string(content))
}

func TestExtractDecisions(t *testing.T) {
	output := `Working on the task...
KODAMA_DECISION: Using Chi router for HTTP layer
Some intermediate output
KODAMA_DECISION: Using modernc.org/sqlite for database
KODAMA_DONE: All done`

	decisions := ExtractDecisions(output)
	assert.Len(t, decisions, 2)
	assert.Equal(t, "Using Chi router for HTTP layer", decisions[0])
	assert.Equal(t, "Using modernc.org/sqlite for database", decisions[1])
}

func TestExtractDoneSummary(t *testing.T) {
	output := "blah blah\nKODAMA_DONE: Task completed with all tests passing\nmore output"
	summary := ExtractDoneSummary(output)
	assert.Equal(t, "Task completed with all tests passing", summary)
}

func TestExtractDoneSummaryMissing(t *testing.T) {
	output := "no done signal here"
	summary := ExtractDoneSummary(output)
	assert.Equal(t, "", summary)
}

func TestExtractChecklist(t *testing.T) {
	output := `Planning the implementation:
- [x] Step 1: Setup
- [x] Step 2: Implementation
- [ ] Step 3: Testing
- [ ] Step 4: Documentation
Some other text`

	checklist := ExtractChecklist(output)
	lines := strings.Split(checklist, "\n")
	assert.Len(t, lines, 4)
	assert.Contains(t, checklist, "- [x] Step 1: Setup")
	assert.Contains(t, checklist, "- [ ] Step 3: Testing")
}

func TestUpdateKodamaMd(t *testing.T) {
	dir := t.TempDir()
	err := InitProject(dir, "Test", "PRD", "", "claude", false)
	require.NoError(t, err)

	decisions := []string{"Using PostgreSQL", "Using Chi router"}
	doneSummary := "Implemented authentication"

	err = UpdateKodamaMd(dir, decisions, doneSummary)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "kodama.md"))
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "Using PostgreSQL")
	assert.Contains(t, s, "Using Chi router")
	assert.Contains(t, s, "Implemented authentication")
}

func TestUpdateKodamaMdMissing(t *testing.T) {
	// Should not error if kodama.md doesn't exist.
	err := UpdateKodamaMd(t.TempDir(), nil, "")
	assert.NoError(t, err)
}
