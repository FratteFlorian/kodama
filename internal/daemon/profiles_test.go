package daemon

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyTaskProfile(t *testing.T) {
	got := applyTaskProfile("Implement login flow.", "ux-reviewer")
	require.Contains(t, got, "Execution profile: ux-reviewer")
	require.Contains(t, got, "Evaluate user flows")
	require.Contains(t, got, "Task:\nImplement login flow.")
}

func TestApplyTaskProfileUnknownOrEmpty(t *testing.T) {
	require.Equal(t, "do work", applyTaskProfile("do work", ""))
	require.Equal(t, "do work", applyTaskProfile("do work", "unknown"))
}

func TestApplyTaskProfileNormalisesCase(t *testing.T) {
	got := applyTaskProfile("do work", " QA ")
	require.True(t, strings.HasPrefix(got, "Execution profile: qa"))
}
