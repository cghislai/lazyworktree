package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chmouel/lazyworktree/internal/app/services"
	"github.com/chmouel/lazyworktree/internal/models"
)

const (
	testFeat         = "feat"
	testGitCmd       = "git"
	testGitPullArg   = "pull"
	testGitPushArg   = "push"
	testRemoteOrigin = "origin"
	testUpstreamRef  = "origin/feature"
	testOtherBranch  = "origin/other"
	testWt1          = "wt1"
	testWt2          = "wt2"
	testReadme       = "README.md"
	testFilterQuery  = "test"
)

func seedClaudeAgentSession(t *testing.T, m *Model, worktreePath string, open bool) {
	t.Helper()
	seedClaudeAgentSessions(t, m, worktreePath, []bool{open})
}

func seedClaudeAgentSessions(t *testing.T, m *Model, worktreePath string, openStates []bool) {
	t.Helper()
	root := t.TempDir()
	m.state.services.agentSessions = services.NewAgentSessionServiceWithStore(
		root,
		"",
		services.NewTestSessionRegistryStore(filepath.Join(root, "registry.json")),
		nil,
	)

	processes := make([]*services.AgentProcess, 0, len(openStates))
	for i, open := range openStates {
		projectDir := filepath.Join(root, "tmp-wt-agent-"+strconv.Itoa(i))
		if err := os.MkdirAll(projectDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		jsonlPath := filepath.Join(projectDir, "session.jsonl")
		entryTime := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
		content := `{"type":"assistant","timestamp":"2026-03-11T10:00:00Z","cwd":"` + worktreePath + `","gitBranch":"feat","message":{"role":"assistant","model":"claude-sonnet","content":[{"type":"text","text":"Done ` + strconv.Itoa(i) + `"}]}}`
		if err := os.WriteFile(jsonlPath, []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		// Align the file mtime with the transcript's own timestamp so offline
		// sessions are not treated as freshly written (active) by mtime liveness.
		if err := os.Chtimes(jsonlPath, entryTime, entryTime); err != nil {
			t.Fatalf("chtimes jsonl: %v", err)
		}
		if open {
			processes = append(processes, &services.AgentProcess{
				PID:       42 + i,
				Agent:     models.AgentKindClaude,
				OpenFiles: []string{jsonlPath},
				CWD:       worktreePath,
			})
		}
	}
	if _, err := m.state.services.agentSessions.RefreshWithProcesses(processes); err != nil {
		t.Fatalf("refresh agent sessions: %v", err)
	}
	m.refreshSelectedWorktreeAgentSessionsPane()
}
