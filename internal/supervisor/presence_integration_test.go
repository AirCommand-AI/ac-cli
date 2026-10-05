package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestAttachedSignalsSurviveStreamReconnect(t *testing.T) {
	m, _, _, _ := setup(t)
	claim, err := m.Claim("agm_queue", "", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_queue", Name: "queued", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid())}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sendAttachedSignal(m.agents["queued"], "interrupt", "body")
	m.mu.Unlock()
	signals, stop, err := m.SubscribeSignals("agm_queue")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-signals:
		if sig.Type != "interrupt" || sig.Text != "body" {
			t.Fatalf("queued signal: %+v", sig)
		}
	default:
		t.Fatal("queued interrupt lost before first stream")
	}
	stop()
	signals, stop, err = m.SubscribeSignals("agm_queue")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-signals:
		t.Fatal("queued interrupt replayed twice")
	default:
	}
	m.ReleaseClaim(claim)
}
func TestAttachedStateEngineReportsAndNudgesThroughStream(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, now := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_state", WorkstreamCode: "478", APIToken: "agent-token", SocketAddress: "ac:agm_state"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var states []agentstate.State
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" || r.Method != "PUT" || r.URL.Path != "/agent/v1/workstreams/478/agents/me/state" {
			t.Errorf("unexpected state route/auth %s %s", r.Method, r.URL.Path)
		}
		var state agentstate.State
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			t.Error(err)
		}
		mu.Lock()
		states = append(states, state)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m.StateReport = func(ctx context.Context, d AgentDefinition, state agentstate.State, at time.Time) error {
		return (agentstate.Reporter{BaseURL: server.URL, Client: server.Client(), Token: "agent-token"}).Report(ctx, d.Workstream, state, at)
	}
	claim, err := m.Claim("agm_state", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_state", Name: "person", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), SessionID: "pi-id"}); err != nil {
		t.Fatal(err)
	}
	signals, cancelSignals, err := m.SubscribeSignals("agm_state")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSignals()
	assert := func(physical agentstate.Physical, logical agentstate.Logical) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(states) == 0 || states[len(states)-1].Physical != physical || states[len(states)-1].Logical != logical {
			t.Fatalf("states: %+v; want %s/%s", states, physical, logical)
		}
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assert(agentstate.Running, agentstate.Idle)
	if len(poll.states) != 0 {
		t.Fatal("legacy state API called")
	}
	if err = m.SessionEvent("agm_state", "run_start", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assert(agentstate.Running, agentstate.Working)
	if idle, err := m.IdleSince(ctx); err != nil || idle != nil {
		t.Fatalf("in-run attached session considered idle: %v %v", idle, err)
	}
	*now = now.Add(16 * time.Minute)
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assert(agentstate.Running, agentstate.NoActivity)
	*now = now.Add(15 * time.Minute)
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-signals:
		if n.Type != "nudge" || n.Text == "" {
			t.Fatalf("nudge: %+v", n)
		}
	default:
		t.Fatal("no attached nudge at 15m no_activity")
	}
	if err = m.SessionEvent("agm_state", "run_end", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assert(agentstate.Running, agentstate.Idle)
	poll.err = ErrAgentStopped
	m.mu.Lock()
	m.agents["person"].nextPoll = time.Time{}
	m.mu.Unlock()
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	assert(agentstate.Stopped, "")
	if tm.kills != 0 || len(tm.launches) != 0 {
		t.Fatal("dashboard stop signalled person's pi")
	}
	m.ReleaseClaim(claim)
}
