package supervisor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

type presencePoll struct {
	*fakePoll
	approval, blocked, inFlight string
}

func (p *presencePoll) PresenceTasks(context.Context, AgentDefinition) (string, string, string, error) {
	return p.approval, p.blocked, p.inFlight, nil
}
func TestPresenceTaskPrecedenceAndWaitingNudge(t *testing.T) {
	m, _, base, now := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_tasks", WorkstreamCode: "478", APIToken: "token", SocketAddress: "ac:agm_tasks"}); err != nil {
		t.Fatal(err)
	}
	p := &presencePoll{fakePoll: base, approval: "6", inFlight: "13"}
	m.Poll = p
	m.StateReport = func(context.Context, AgentDefinition, agentstate.State, time.Time) error { return nil }
	claim, err := m.Claim("agm_tasks", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_tasks", Name: "tasks", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid())}); err != nil {
		t.Fatal(err)
	}
	signals, cancel, err := m.SubscribeSignals("agm_tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	check := func(reason string) {
		t.Helper()
		if err := m.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		state := m.agents["tasks"].presence.State
		m.mu.Unlock()
		if state.Logical != agentstate.Waiting || state.Reason != reason {
			t.Fatalf("waiting state %+v, want %s", state, reason)
		}
	}
	check("approval for #6")
	p.approval = ""
	p.blocked = "12"
	m.mu.Lock()
	m.agents["tasks"].nextTaskCheck = time.Time{}
	m.mu.Unlock()
	check("#12 blocked")
	p.blocked = ""
	m.mu.Lock()
	m.agents["tasks"].nextTaskCheck = time.Time{}
	m.mu.Unlock()
	check("#13 in flight")
	*now = now.Add(15 * time.Minute)
	check("#13 in flight")
	select {
	case sig := <-signals:
		if sig.Type != "nudge" {
			t.Fatalf("nudge %v", sig)
		}
	default:
		t.Fatal("in-flight waiting nudge missing")
	}
	if err = m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signals:
		t.Fatal("duplicate waiting nudge")
	default:
	}
	m.ReleaseClaim(claim)
}
