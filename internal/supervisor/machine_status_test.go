package supervisor

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

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
