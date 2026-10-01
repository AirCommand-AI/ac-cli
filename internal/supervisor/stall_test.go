package supervisor

import (
	"context"
	"errors"
	"fmt"
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
	failBodyAlways bool
	bodyStatus     int
	inFlightErr    error
	onInFlight     func()
	onReport       func(string)
	updates        []string
}

func (p *fakeStallPoll) InFlight(context.Context, AgentDefinition) (InFlightTask, bool, bool, error) {
	p.checks++
	if p.onInFlight != nil {
		p.onInFlight()
	}
	if p.inFlightErr != nil {
		return InFlightTask{}, false, false, p.inFlightErr
	}
	return p.task, p.found, p.pending, nil
}
func (p *fakeStallPoll) MessageBody(_ context.Context, _ AgentDefinition, _ string) (string, error) {
	if p.bodyStatus != 0 {
		return "", &APIStatusError{Status: p.bodyStatus}
	}
	if p.failBodyAlways {
		return "", errors.New("unavailable")
	}
	if p.failBodyOnce {
		p.failBodyOnce = false
		return "", errors.New("temporary body fetch failure")
	}
	return p.body, nil
}
func (p *fakeStallPoll) NudgeUpdate(_ context.Context, _ AgentDefinition, task InFlightTask) error {
	p.updates = append(p.updates, task.ID)
	return nil
}
func (p *fakeStallPoll) StateReason(_ context.Context, _ AgentDefinition, state, reason string) error {
	p.reports = append(p.reports, state+":"+reason)
	if p.onReport != nil {
		p.onReport(state)
	}
	return nil
}

func TestPiProgressEventClearsStallAndReportsCurrentState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		settled bool
		want    string
	}{{"model", false, "working"}, {"settled", true, "idle"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _, _, now := setup(t)
			p := &fakeStallPoll{task: InFlightTask{ID: "task-1", Number: 1}, found: true}
			m.Poll = p
			d := definition(m.Home)
			d.Mode = "headless"
			f := pidriver.NewFake()
			m.NewDriver = func(io.Writer) pidriver.Driver { return f }
			if err := m.Start(ctx, d); err != nil {
				t.Fatal(err)
			}
			m.agents[d.Name].nextPoll = now.Add(time.Hour)
			f.Snapshot = pidriver.Snapshot{Ready: true, Streaming: !tc.settled, Settled: tc.settled, LastEvent: now.Add(-16 * time.Minute)}
			if err := m.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if !m.agents[d.Name].stalled {
				t.Fatal("did not enter stalled state")
			}
			f.Snapshot.LastEvent = *now
			f.EventCh <- pidriver.Event{Kind: "message_update", At: *now}
			deadline := time.After(time.Second)
			for {
				p.fakePoll.mu.Lock()
				states := append([]string(nil), p.fakePoll.states...)
				p.fakePoll.mu.Unlock()
				m.mu.Lock()
				stalled := m.agents[d.Name].stalled
				m.mu.Unlock()
				if !stalled && len(states) > 0 {
					if states[len(states)-1] != tc.want {
						t.Fatalf("reported %v, want %s", states, tc.want)
					}
					break
				}
				select {
				case <-deadline:
					t.Fatalf("pi progress did not clear stall: states=%v stalled=%v", states, stalled)
				case <-time.After(time.Millisecond):
				}
			}
			if err := m.Stop(ctx, d.Name); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdleWithoutTaskChecksAtFiveMinuteIntervals(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	p := &fakeStallPoll{}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	f.Snapshot = pidriver.Snapshot{Ready: true, Settled: true, LastEvent: now.Add(-16 * time.Minute)}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		*now = now.Add(time.Minute)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if p.checks != 1 {
		t.Fatalf("idle without task polled %d times within 5m", p.checks)
	}
	*now = now.Add(time.Minute)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if p.checks != 2 {
		t.Fatalf("did not check after 5m: %d", p.checks)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}

func TestStallCheckErrorsAndRemovedAgentDoNotReport(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx := context.Background()
			m, _, _, now := setup(t)
			p := &fakeStallPoll{task: InFlightTask{ID: "task-1", Number: 1}, found: true}
			if stop {
				p.onInFlight = func() {
					if err := m.Stop(ctx, "eng-1"); err != nil {
						t.Error(err)
					}
				}
			} else {
				p.inFlightErr = errors.New("approval check failed")
			}
			m.Poll = p
			d := definition(m.Home)
			d.Mode = "headless"
			f := pidriver.NewFake()
			m.NewDriver = func(io.Writer) pidriver.Driver { return f }
			if err := m.Start(ctx, d); err != nil {
				t.Fatal(err)
			}
			m.agents[d.Name].nextPoll = now.Add(time.Hour)
			f.Snapshot = pidriver.Snapshot{Ready: true, Settled: true, LastEvent: now.Add(-16 * time.Minute)}
			if err := m.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if len(p.reports) != 0 || len(f.Sent) != 1 || m.agents[d.Name].stalled {
				t.Fatalf("false stall after error/stop: %+v", p.reports)
			}
			if !stop {
				if err := m.Stop(ctx, d.Name); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStallReportRaceRestoresCurrentState(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	p := &fakeStallPoll{}
	m.Poll = p
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	m.agents[d.Name].nextPoll = now.Add(time.Hour)
	f.Snapshot = pidriver.Snapshot{Ready: true, Streaming: true, LastEvent: now.Add(-16 * time.Minute)}
	p.onReport = func(state string) {
		if state == "stalled" {
			m.mu.Lock()
			m.agents[d.Name].stalled = false
			m.mu.Unlock()
		}
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.reports) != 2 || p.reports[0] != "stalled:model call silent 15m" || p.reports[1] != "working:" {
		t.Fatalf("stale stalled report not restored: %v", p.reports)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
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
	if len(f.Sent) != 2 || f.Sent[1].Kind != pidriver.Interrupt || !strings.Contains(f.Sent[1].Text, "Interrupt body: "+p.body) || !strings.Contains(f.Sent[1].Text, n.MessageID) {
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
	if len(f.Sent) != 3 || f.Sent[1].Kind != pidriver.Interrupt || !strings.Contains(f.Sent[1].Text, "Interrupt body: Stop now") || f.Sent[2].Source != regular.MessageID {
		t.Fatalf("retry order: %+v", f.Sent)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}

func TestManualNudgeWakeUsesUrgentPointer(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "ac_operator", SenderNature: "human", Kind: "nudge", Priority: "urgent"}
	if err := m.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 2 || f.Sent[1].Kind != pidriver.Urgent || f.Sent[1].Source != n.MessageID {
		t.Fatalf("manual nudge delivery: %+v", f.Sent)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentInterruptFetchFallsBackToUrgentPointer(t *testing.T) {
	for _, status := range []int{404, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx := context.Background()
			m, _, _, now := setup(t)
			p := &fakeStallPoll{bodyStatus: status, failBodyAlways: status == 0}
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
			for i := 0; i < 3 && len(f.Sent) == 1; i++ {
				if err := m.Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if len(f.Sent) != 2 || f.Sent[1].Kind != pidriver.Urgent || !strings.Contains(f.Sent[1].Text, "--message '"+n.MessageID+"'") {
				t.Fatalf("no pointer fallback: %+v", f.Sent)
			}
			if len(m.agents[d.Name].pendingWakes) != 0 {
				t.Fatal("permanent failure blocked later wakes")
			}
			if err := m.Stop(ctx, d.Name); err != nil {
				t.Fatal(err)
			}
		})
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
	if delay := m.agents[d.Name].nextStallCheck.Sub(*now); delay != 5*time.Minute {
		t.Fatalf("pending approval check interval %s, want 5m", delay)
	}
	*now = now.Add(4 * time.Minute)
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.checks != 1 {
		t.Fatalf("pending approval checked too often: %d", p.checks)
	}
	*now = now.Add(-4 * time.Minute)
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
	if len(p.updates) != 1 || p.updates[0] != "task-1" {
		t.Fatalf("missing best-effort auto-nudge update: %v", p.updates)
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
	m2, _, _, _ := setup(t)
	m2.Home = m.Home
	m2.Now = func() time.Time { return *now }
	m2.Poll = p
	f2 := pidriver.NewFake()
	m2.NewDriver = func(io.Writer) pidriver.Driver { return f2 }
	m2.mu.Lock()
	err := m2.bootAgent(context.Background(), m.definitionPath(d.AgentID))
	m2.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	m2.agents[d.Name].nextPoll = now.Add(time.Hour)
	f2.Snapshot = pidriver.Snapshot{Ready: true, Settled: true, LastEvent: now.Add(-16 * time.Minute)}
	if err := m2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f2.Sent) != 1 || m2.agents[d.Name].def.Nudge == nil || m2.agents[d.Name].def.Nudge.TaskID != "task-2" {
		t.Fatalf("persisted guard lost on restart: %+v", f2.Sent)
	}
	if err := m2.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
}
