package supervisor

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

type fakeStallPoll struct {
	fakePoll
	task           InFlightTask
	found, pending bool
	reports        []string
	checks         int
	body           string
}

func (p *fakeStallPoll) InFlight(context.Context, AgentDefinition) (InFlightTask, bool, bool, error) {
	p.checks++
	return p.task, p.found, p.pending, nil
}
func (p *fakeStallPoll) MessageBody(_ context.Context, _ AgentDefinition, _ string) (string, error) {
	return p.body, nil
}
func (p *fakeStallPoll) StateReason(_ context.Context, _ AgentDefinition, state, reason string) error {
	p.reports = append(p.reports, state+":"+reason)
	return nil
}

func TestInterruptFetchesBodyAndUsesDriverInterrupt(t *testing.T) {
	m, _, _, _ := setup(t)
	p := &fakeStallPoll{body: "Stop that operation and inspect the result"}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "ac_operator", SenderNature: "human", Kind: "interrupt", Priority: "urgent"}
	if err := m.Wake(context.Background(), d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 2 || f.Sent[1].Kind != pidriver.Interrupt || f.Sent[1].Text != p.body {
		t.Fatalf("interrupt delivery: %+v", f.Sent)
	}
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
}

func TestSilentToolStallsWithoutNudgeAndRefreshes(t *testing.T) {
	m, _, _, now := setup(t)
	p := &fakeStallPoll{}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	f.Snapshot = pidriver.Snapshot{Ready: true, Streaming: true, CurrentTool: "shell", LastEvent: now.Add(-16 * time.Minute)}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 1 || p.reports[0] != "stalled:tool shell silent 15m" || p.checks != 0 || len(f.Sent) != 1 {
		t.Fatalf("silent tool: reports=%v checks=%d prompts=%+v", p.reports, p.checks, f.Sent)
	}
	*now = now.Add(10 * time.Minute)
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 2 {
		t.Fatalf("stalled not refreshed at 10m: %v", p.reports)
	}
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
}
func TestSettledInFlightNudgesOnceAndPendingApprovalSuppresses(t *testing.T) {
	m, _, _, now := setup(t)
	p := &fakeStallPoll{task: InFlightTask{ID: "task-1", Number: 7}, found: true, pending: true}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	f.Snapshot = pidriver.Snapshot{Ready: true, Settled: true, LastEvent: now.Add(-16 * time.Minute)}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 0 || len(f.Sent) != 1 {
		t.Fatal("pending approval was stalled")
	}
	p.pending = false
	m.agents[d.Name].nextStallCheck = time.Time{}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 2 || f.Sent[1].Kind != pidriver.Urgent || !strings.Contains(f.Sent[1].Text, "task #7") {
		t.Fatalf("missing H5 nudge: %+v", f.Sent)
	}
	if m.agents[d.Name].def.Nudge == nil || m.agents[d.Name].def.Nudge.TaskID != "task-1" {
		t.Fatal("nudge guard not persisted")
	}
	*now = now.Add(20 * time.Minute)
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 2 {
		t.Fatal("same task auto-nudged twice")
	}
	if len(p.reports) < 2 || !strings.HasPrefix(p.reports[0], "stalled:idle 15m") {
		t.Fatalf("stall reports: %v", p.reports)
	}
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
}
