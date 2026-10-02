package supervisor

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

type pausingStopDriver struct {
	*pidriver.Fake
	entered, release chan struct{}
}

func (d *pausingStopDriver) Stop(ctx context.Context) error {
	close(d.entered)
	select {
	case <-d.release:
		return d.Fake.Stop(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestTakeoverHidesDriverBeforeBlockingStop(t *testing.T) {
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	driver := &pausingStopDriver{Fake: pidriver.NewFake(), entered: make(chan struct{}), release: make(chan struct{})}
	m.NewDriver = func(io.Writer) pidriver.Driver { return driver }
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Takeover(context.Background(), d.Name); done <- err }()
	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("Stop not entered")
	}
	if _, ok := m.Driver(d.Name); ok {
		t.Fatal("stopping driver still published")
	}
	close(driver.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestTakeoverPersistsPIDAndRefusesMutations(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Takeover(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordTakeover(d.Name, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	a := m.agents[d.Name]
	if a.def.Takeover == nil || !takeoverAlive(a.def.Takeover) {
		t.Fatal("pid/starttime not persisted")
	}
	for _, err := range []error{m.Start(ctx, d), m.Stop(ctx, d.Name), m.Mode(ctx, d.Name, "tmux"), m.Remove(ctx, d.Name), m.ResumeTakeover(d.Name)} {
		if err == nil || !strings.Contains(err.Error(), "taken over") && !strings.Contains(err.Error(), "still running") {
			t.Fatalf("mutation during takeover: %v", err)
		}
	}
	a.def.Takeover.StartTime = "stale"
	if err := m.ResumeTakeover(d.Name); err != nil {
		t.Fatal(err)
	}
}
func TestBootGracesTakeoverWithoutRecordedPID(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Takeover(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	m.release(m.agents[d.Name])
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	next.Now = func() time.Time { return *now }
	next.NewDriver = m.NewDriver
	next.mu.Lock()
	err := next.bootAgent(ctx, m.definitionPath(d.AgentID))
	next.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a := next.agents[d.Name]
	if !a.takenOver || a.driver != nil {
		t.Fatal("restarted daemon ignored no-pid grace")
	}
	a.nextPoll = now.Add(time.Hour)
	if err := next.Tick(ctx); err != nil || !a.takenOver {
		t.Fatalf("grace lost early: %v", err)
	}
	*now = now.Add(31 * time.Second)
	if err := next.Tick(ctx); err != nil || a.takenOver {
		t.Fatalf("stale no-pid takeover not released: %v", err)
	}
}
func TestTakeoverSurvivesDaemonRestart(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Takeover(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordTakeover(d.Name, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	m.release(m.agents[d.Name]) // simulate old daemon exiting without stopping foreground pi
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	next.NewDriver = m.NewDriver
	next.mu.Lock()
	err := next.bootAgent(ctx, m.definitionPath(d.AgentID))
	next.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a := next.agents[d.Name]
	if !a.takenOver || a.def.Takeover == nil || a.driver != nil {
		t.Fatal("restarted daemon reopened foreground session")
	}
	a.nextPoll = next.now().Add(time.Hour)
	if err := next.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if a.driver != nil {
		t.Fatal("tick launched second pi")
	}
	a.def.Takeover.StartTime = "reused"
	if err := next.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if a.takenOver {
		t.Fatal("expired pid fence not cleared")
	}
}
func TestTakeoverFencesHeadlessAndResumesSameSession(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	var drivers []*pidriver.Fake
	m.NewDriver = func(io.Writer) pidriver.Driver { f := pidriver.NewFake(); drivers = append(drivers, f); return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	spec, err := m.Takeover(ctx, d.Name)
	if err != nil {
		t.Fatal(err)
	}
	if spec.SessionID != d.AgentID || spec.WorkDir != d.WorkFolder || spec.PiPath != m.Pi {
		t.Fatalf("launch spec: %+v", spec)
	}
	for _, arg := range spec.Args {
		if arg == "--aircommand-headless" {
			t.Fatal("foreground pi has headless extension flag")
		}
	}
	a := m.agents[d.Name]
	if !a.takenOver || a.driver != nil || a.def.State != "taken-over" || a.lock == nil {
		t.Fatal("takeover did not fence headless session")
	}
	a.nextPoll = now.Add(time.Hour)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 {
		t.Fatal("headless relaunched during takeover")
	}
	if err := m.ResumeTakeover(d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 2 || drivers[1].Launches[0].SessionID != d.AgentID {
		t.Fatal("headless did not resume exact session")
	}
}
