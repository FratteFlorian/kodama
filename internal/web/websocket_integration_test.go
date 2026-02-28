package web

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/florian/kodama/internal/agent"
	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/daemon"
	"github.com/florian/kodama/internal/db"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type wsFakeAgent struct {
	output chan string
}

func newWSFakeAgent(lines ...string) *wsFakeAgent {
	ch := make(chan string, 16)
	go func() {
		for _, l := range lines {
			ch <- l + "\n"
		}
		close(ch)
	}()
	return &wsFakeAgent{output: ch}
}

func (f *wsFakeAgent) Start(workdir, task, contextFile string) error { return nil }
func (f *wsFakeAgent) Write(input string) error                      { return nil }
func (f *wsFakeAgent) Output() <-chan string                         { return f.output }
func (f *wsFakeAgent) Detect(line string) (agent.Signal, string)     { return agent.ParseSignal(line) }
func (f *wsFakeAgent) Stop() error                                   { return nil }
func (f *wsFakeAgent) SessionID() string                             { return "" }
func (f *wsFakeAgent) CostUSD() float64                              { return 0 }
func (f *wsFakeAgent) TokensUsed() (int64, int64)                    { return 0, 0 }
func (f *wsFakeAgent) LastError() error                              { return nil }

func TestWebSocketStreamsTaskOutput(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()
	require.NoError(t, database.UpsertSettings("", 0))

	cfg := &config.Config{Port: 0, QuestionTimeout: 2 * time.Second}
	hub := NewHub()
	envHub := NewHub()
	d := daemon.New(cfg, database, hub, envHub)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	task, err := database.CreateTask(proj.ID, "do work", "", 0, false)
	require.NoError(t, err)

	d.SetAgentFactory(func(task *db.Task, proj *db.Project) (agent.Agent, string) {
		return newWSFakeAgent("hello from agent", "KODAMA_DONE: ok"), "codex"
	})

	srv, err := New(cfg, database, hub, envHub, d)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("websocket test requires tcp listener: %v", err)
	}
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Listener = ln
	ts.Start()
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/tasks/" + itoa(task.ID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, d.StartProject(context.Background(), proj.ID))

	var got string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(got, "hello from agent") {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		got += string(msg)
	}
	require.Contains(t, got, "hello from agent")
}
