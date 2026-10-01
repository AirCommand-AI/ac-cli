package supervisor

import (
	"context"
	"errors"
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
	failBodyOnce   bool
}

func (p *fakeStallPoll) InFlight(context.Context, AgentDefinition) (InFlightTask, bool, bool, error) {
	p.checks++
	return p.task, p.found, p.pending, nil
}
func (p *fakeStallPoll) MessageBody(_ context.Context, _ AgentDefinition, _ string) (string, error) {
	if p.failBodyOnce {
		p.failBodyOnce = false
		return "", errors.New("temporary body fetch failure")
	}
	return p.body, nil
}
func (p *fakeStallPoll) StateReason(_ context.Context, _ AgentDefinition, state, reason string) error {
	p.reports = append(p.reports, state+":"+reason)
	return nil
}

func TestStallReasonRulesAndProgress(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	task := InFlightTask{ID: "task1", Number: 9}
	for _, tc := range []struct {
		name         string
		snap         pidriver.Snapshot
		has, pending bool
		reason       string
		nudge        bool
	}{
		{"silent model", pidriver.Snapshot{Streaming: true, LastEvent: now.Add(-16 * time.Minute)}, true, false, "model call silent 15m", false},
		{"silent tool", pidriver.Snapshot{Streaming: true, CurrentTool: "bash", LastEvent: now.Add(-16 * time.Minute)}, true, false, "tool bash silent 15m", false},
		{"idle task", pidriver.Snapshot{Settled: true, LastEvent: now.Add(-16 * time.Minute)}, true, false, "idle 15m on task #9", true},
		{"pending approval", pidriver.Snapshot{Settled: true, LastEvent: now.Add(-16 * time.Minute)}, true, true, "", false},
		{"no task", pidriver.Snapshot{Settled: true, LastEvent: now.Add(-16 * time.Minute)}, false, false, "", false},
		{"recent token", pidriver.Snapshot{Streaming: true, LastEvent: now.Add(-time.Minute)}, true, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, nudge := stallReason(tc.snap, now, task, tc.has, tc.pending)
			if reason != tc.reason || nudge != tc.nudge {
				t.Fatalf("got %q/%v want %q/%v", reason, nudge, tc.reason, tc.nudge)
			}
		})
	}
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

func TestInterruptBodyFetchRetriesWithoutOvertakingQueuedWake(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	p := &fakeStallPoll{body: "Stop now", failBodyOnce: true}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "ac_operator", SenderNature: "human", Kind: "interrupt", Priority: "urgent"}
	if err := m.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 || len(m.agents[d.Name].pendingWakes) != 1 {
		t.Fatal("failed body fetch was dropped")
	}
	regular := Notification{Type: "message.received", MessageID: "fedcba9876543210", SenderID: "agm_lead", SenderNature: "agent"}
	if err := m.Wake(ctx, d.AgentID, regular); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 || len(m.agents[d.Name].pendingWakes) != 2 {
		t.Fatal("new wake overtook interrupt")
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 3 || f.Sent[1].Kind != pidriver.Interrupt || f.Sent[1].Text != "Stop now" || f.Sent[2].Source != regular.MessageID {
		t.Fatalf("retry order: %+v", f.Sent)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
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
	p.task = InFlightTask{ID: "task-2", Number: 8}
	m.agents[d.Name].nextStallCheck = time.Time{}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.agents[d.Name].stalled || len(f.Sent) != 2 {
		t.Fatal("changing task did not end stall")
	}
	*now = now.Add(15 * time.Minute)
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 3 || !strings.Contains(f.Sent[2].Text, "task #8") {
		t.Fatalf("new task was not nudged: %+v", f.Sent)
	}
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
}
