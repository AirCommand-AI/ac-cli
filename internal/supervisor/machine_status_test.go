package supervisor

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestMachineHoldPreservesParkedStates(t *testing.T) {
	for _, state := range []string{"crashed", "stopped-by-dashboard"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			m, _, _, _ := setup(t)
			d := definition(m.Home)
			d.Mode = "headless"
			m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
			if err := m.Start(ctx, d); err != nil {
				t.Fatal(err)
			}
			m.agents[d.Name].def.State = state
			if err := m.SetMachineState(ctx, "stopping"); err != nil {
				t.Fatal(err)
			}
			if m.agents[d.Name].def.State != state || !m.AgentsStopped() {
				t.Fatalf("parked state %q stopped %v", m.agents[d.Name].def.State, m.AgentsStopped())
			}
			statuses, err := m.List(ctx)
			if err != nil || len(statuses) != 1 || statuses[0].State != "stopped" {
				t.Fatalf("hold status %+v %v", statuses, err)
			}
			if err := m.SetMachineState(ctx, "online"); err != nil {
				t.Fatal(err)
			}
			if err := m.Tick(ctx); err != nil || m.agents[d.Name].driver != nil {
				t.Fatalf("parked agent relaunched: %v", err)
			}
			statuses, err = m.List(ctx)
			if err != nil || statuses[0].State != state {
				t.Fatalf("parked status after hold %+v %v", statuses, err)
			}
		})
	}
}
func TestDeadTakeoverClearsDuringStoppingHold(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Takeover(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMachineState(ctx, "stopping"); err == nil || m.AgentsStopped() {
		t.Fatalf("unfenced takeover marked stopped: %v", err)
	}
	m.agents[d.Name].takeoverConnected = false
	*now = now.Add(31 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.AgentsStopped() || m.agents[d.Name].driver != nil {
		t.Fatal("dead takeover cleanup did not finish hold")
	}
}
func TestMachineStoppingHoldPersistsAcrossDaemonRestart(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMachineState(ctx, "stopping"); err != nil {
		t.Fatal(err)
	}
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	next.Now = func() time.Time { return *now }
	next.NewDriver = m.NewDriver
	if !next.stoppingHold {
		t.Fatal("machine hold marker lost")
	}
	next.mu.Lock()
	err := next.bootAgent(ctx, m.definitionPath(d.AgentID))
	next.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if next.agents[d.Name].driver != nil {
		t.Fatal("boot relaunched while stopping")
	}
	if err := next.SetMachineState(ctx, "online"); err != nil {
		t.Fatal(err)
	}
	if err := next.Tick(ctx); err != nil || next.agents[d.Name].driver == nil {
		t.Fatalf("online did not resume: %v", err)
	}
}
func TestMachineStoppingHoldPreservesDesiredAndResumes(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMachineState(ctx, "stopping"); err != nil {
		t.Fatal(err)
	}
	if !m.AgentsStopped() || m.agents[d.Name].def.Desired != "running" {
		t.Fatalf("hold state %+v", m.agents[d.Name].def)
	}
	if err := m.Start(ctx, d); err == nil {
		t.Fatal("Start bypassed stopping hold")
	}
	if _, err := m.Takeover(ctx, d.Name); err == nil {
		t.Fatal("Takeover bypassed stopping hold")
	}
	m.agents[d.Name].takenOver = true
	if m.AgentsStopped() {
		t.Fatal("active foreground takeover reported stopped")
	}
	m.agents[d.Name].takenOver = false
	if err := m.Tick(ctx); err != nil || m.agents[d.Name].driver != nil {
		t.Fatalf("hold relaunched pi: %v", err)
	}
	if err := m.SetMachineState(ctx, "online"); err != nil {
		t.Fatal(err)
	}
	if m.AgentsStopped() {
		t.Fatal("hold not released")
	}
	if err := m.Tick(ctx); err != nil || m.agents[d.Name].driver == nil {
		t.Fatalf("online did not resume: %v", err)
	}
	if err := m.SetMachineState(ctx, "stopping"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMachineState(ctx, "error"); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil || m.agents[d.Name].driver == nil {
		t.Fatalf("error did not release hold: %v", err)
	}
}

func TestIdleSinceRemembersStreamingTurnBetweenReports(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	var f *pidriver.Fake
	m.NewDriver = func(io.Writer) pidriver.Driver { f = pidriver.NewFake(); return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	first, err := m.IdleSince(ctx)
	if err != nil || first == nil {
		t.Fatalf("initial idle %v %v", first, err)
	}
	*now = now.Add(5 * time.Minute)
	f.EventCh <- pidriver.Event{Kind: "agent_settled"}
	deadline := time.After(time.Second)
	for {
		m.mu.Lock()
		updated := m.lastBusy.Equal(*now)
		m.mu.Unlock()
		if updated {
			break
		}
		select {
		case <-deadline:
			t.Fatal("settled event not consumed")
		case <-time.After(time.Millisecond):
		}
	}
	idle, err := m.IdleSince(ctx)
	if err != nil || idle == nil || !idle.Equal(*now) {
		t.Fatalf("short streaming turn lost: %v %v", idle, err)
	}
}
func TestIdleSinceUsesTmuxClientAndWindowActivity(t *testing.T) {
	ctx := context.Background()
	m, tm, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "tmux"
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	tm.attached = true
	if idle, err := m.IdleSince(ctx); err != nil || idle != nil {
		t.Fatalf("attached: %v %v", idle, err)
	}
	*now = now.Add(10 * time.Minute)
	tm.attached = false
	tm.activity = now.Add(-time.Minute)
	idle, err := m.IdleSince(ctx)
	if err != nil || idle == nil || !idle.Equal(tm.activity) {
		t.Fatalf("pane activity %v %v, want %v", idle, err, tm.activity)
	}
	*now = now.Add(31 * time.Minute)
	idle, err = m.IdleSince(ctx)
	if err != nil || idle == nil || !idle.Equal(tm.activity) {
		t.Fatalf("idle since changed: %v %v", idle, err)
	}
	tm.activity = now.Add(-time.Minute)
	idle, err = m.IdleSince(ctx)
	if err != nil || idle == nil || !idle.Equal(tm.activity) {
		t.Fatalf("fresh activity: %v %v", idle, err)
	}
}
