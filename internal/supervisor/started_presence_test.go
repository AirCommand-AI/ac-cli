package supervisor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestHeadlessRPCEventsUseK1WithoutLegacyStateCalls(t *testing.T) {
	ctx := context.Background()
	m, _, poll, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, APIToken: "token", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	fake := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return fake }
	var mu sync.Mutex
	var states []agentstate.State
	m.StateReport = func(_ context.Context, _ AgentDefinition, s agentstate.State, _ time.Time) error {
		mu.Lock()
		states = append(states, s)
		mu.Unlock()
		return nil
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	fake.EventCh <- pidriver.Event{Kind: "agent_start", At: time.Now()}
	wait := func(inRun bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			m.mu.Lock()
			got := m.agents[d.Name].presence.InRun
			m.mu.Unlock()
			if got == inRun {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("inRun=%v want %v", got, inRun)
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(true)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(states) == 0 || states[len(states)-1].Logical != agentstate.Working {
		t.Fatalf("state on run_start: %+v", states)
	}
	mu.Unlock()
	fake.EventCh <- pidriver.Event{Kind: "agent_settled", At: time.Now()}
	wait(false)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if states[len(states)-1].Logical != agentstate.Idle {
		t.Fatalf("state on run_end: %+v", states)
	}
	mu.Unlock()
	poll.mu.Lock()
	old := append([]string(nil), poll.states...)
	poll.mu.Unlock()
	if len(old) != 0 {
		t.Fatalf("deprecated state API used: %v", old)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}
