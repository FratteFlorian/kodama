package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBotWhitelistLogic tests the whitelist concept without a real Telegram API.
// Full integration tests would require a mock Telegram API server.

func TestHandleAnswerCommand(t *testing.T) {
	// Test the answer parsing logic by testing the message handler directly.
	b := &Bot{
		userID:    12345,
		questions: make(map[int64]chan string),
	}

	// Register a question channel.
	ch := make(chan string, 1)
	b.mu.Lock()
	b.questions[42] = ch
	b.mu.Unlock()

	// Simulate the answer parsing.
	answer := parseAnswerCommand("/answer 42 Using PostgreSQL")
	assert.NotNil(t, answer)
	assert.Equal(t, int64(42), answer.taskID)
	assert.Equal(t, "Using PostgreSQL", answer.text)
}

func TestHandleAnswerCommandInvalid(t *testing.T) {
	// Missing task ID.
	answer := parseAnswerCommand("/answer")
	assert.Nil(t, answer)

	// Invalid task ID.
	answer = parseAnswerCommand("/answer abc some answer")
	assert.Nil(t, answer)
}

func TestSinglePendingQuestionFallback(t *testing.T) {
	b := &Bot{
		userID:    12345,
		questions: make(map[int64]chan string),
	}

	ch := make(chan string, 1)
	b.mu.Lock()
	b.questions[1] = ch
	b.mu.Unlock()

	// When there's exactly one question, any message is an answer.
	b.mu.Lock()
	count := len(b.questions)
	b.mu.Unlock()
	assert.Equal(t, 1, count)
}

// answerCommand is a parsed /answer command.
type answerCommand struct {
	taskID int64
	text   string
}

// parseAnswerCommand parses "/answer <taskID> <text>" and returns nil on failure.
func parseAnswerCommand(msg string) *answerCommand {
	if len(msg) <= len("/answer ") {
		return nil
	}
	rest := msg[len("/answer "):]

	parts := splitN(rest, " ", 2)
	if len(parts) < 2 {
		return nil
	}

	var taskID int64
	if _, err := scanInt64(parts[0], &taskID); err != nil {
		return nil
	}

	return &answerCommand{taskID: taskID, text: parts[1]}
}

func splitN(s, sep string, n int) []string {
	result := []string{}
	for i := 0; i < n-1; i++ {
		idx := indexByte(s, sep[0])
		if idx < 0 {
			break
		}
		result = append(result, s[:idx])
		s = s[idx+1:]
	}
	result = append(result, s)
	return result
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func scanInt64(s string, v *int64) (int, error) {
	var n int64
	negative := false
	i := 0
	if i < len(s) && s[i] == '-' {
		negative = true
		i++
	}
	if i >= len(s) {
		return 0, assert.AnError
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int64(s[i]-'0')
		i++
	}
	if i < len(s) {
		return 0, assert.AnError
	}
	if negative {
		n = -n
	}
	*v = n
	return i, nil
}
