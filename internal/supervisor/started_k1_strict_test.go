package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestStartedHeadlessK1OnlyAndOneNudgePerEpisode(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	store := credentials.NewStore(m.Home)
	if err := store.Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, APIToken: "secret", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	states := []string{}
	legacy := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/workstreams/626/agents/me/state" || r.Method != "PUT" {
			t.Errorf("unexpected HTTP %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("agent bearer missing")
		}
		var body struct {
			Physical, Logical string
			State             string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if body.Physical == "" || body.State != "" {
			legacy++
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		states = append(states, body.Logical)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	poll := &HTTPPoller{BaseURL: server.URL, Client: server.Client(), Store: store}
	m.Poll = poll
	m.StateReport = poll.ReportState
	fake := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return fake }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	m.agents[d.Name].nextTaskCheck = now.Add(time.Hour)
	m.mu.Unlock()
	fake.EventCh <- pidriver.Event{Kind: "agent_start"}
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		running := m.agents[d.Name].presence.InRun
		m.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RPC start not consumed")
		}
		time.Sleep(time.Millisecond)
	}
	for _, advance := range []time.Duration{0, 16 * time.Minute, 15 * time.Minute, time.Second} {
		*now = now.Add(advance)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	seen := append([]string(nil), states...)
	invalid := legacy
	mu.Unlock()
	if invalid != 0 || len(seen) < 2 || seen[0] != "working" || seen[1] != "no_activity" {
		t.Fatalf("state PUTs: %v, legacy=%d", seen, invalid)
	}
	count := 0
	for _, sent := range fake.Sent {
		if sent.Source == "auto-nudge" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("automatic nudges = %d, want exactly one", count)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}
