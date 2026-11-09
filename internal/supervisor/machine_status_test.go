package supervisor

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestRunFinishingHoldSurvivesRestart(t *testing.T) {
	m, tm, poll, _ := setup(t)
	if err := os.MkdirAll(storagepath.Root(m.Home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagepath.Root(m.Home), "run.json"), []byte(`{"runId":"run_example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.BeginRunFinishing(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	if !next.runFinishing || !next.stoppingHold {
		t.Fatal("run finishing hold did not survive restart")
	}
}

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
			if err := m.holdAndStop(ctx); err != nil {
				t.Fatal(err)
			}
			if m.agents[d.Name].def.State != state || !m.AgentsStopped() {
				t.Fatalf("parked state %q stopped %v", m.agents[d.Name].def.State, m.AgentsStopped())
			}
			statuses, err := m.List(ctx)
			if err != nil || len(statuses) != 1 || statuses[0].State != "stopped" {
				t.Fatalf("hold status %+v %v", statuses, err)
			}
			if err := m.Tick(ctx); err != nil || m.agents[d.Name].driver != nil {
				t.Fatalf("parked agent relaunched: %v", err)
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
	if err := m.holdAndStop(ctx); err == nil || m.AgentsStopped() {
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
	if err := os.MkdirAll(storagepath.Root(m.Home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagepath.Root(m.Home), "run.json"), []byte(`{"runId":"run_example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.holdAndStop(ctx); err != nil {
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
}

func TestStaleHoldMarkerWithoutRunIsCleared(t *testing.T) {
	m, tm, poll, _ := setup(t)
	if err := os.MkdirAll(filepath.Dir(m.machineHoldPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.machineHoldPath(), []byte("stopping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	if next.stoppingHold || next.runFinishing {
		t.Fatal("stale hold kept agents stopped on a machine with no run")
	}
	if _, err := os.Stat(m.machineHoldPath()); !os.IsNotExist(err) {
		t.Fatalf("stale hold marker not removed: %v", err)
	}
}
func TestMachineStoppingHoldPreservesDesired(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.holdAndStop(ctx); err != nil {
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
}
