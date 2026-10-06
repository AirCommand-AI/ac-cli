package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
)

// An event processed during a pass can carry a later time than the pass
// start; the report must never be stamped before its own Since.
func TestPresenceReportIsNeverStampedBeforeItsSince(t *testing.T) {
	m, _, _, now := setup(t)
	later := now.Add(5 * time.Second)
	presence := agentstate.New("pi", *now)
	presence, _ = agentstate.Step(presence, agentstate.Event{Kind: "session_alive", At: later})
	a := &managed{def: AgentDefinition{AgentID: "agm_at", Name: "stamp", Workstream: "478", State: "running", Desired: "running", Kind: "attached"}, presence: presence, nextTaskCheck: now.Add(time.Hour)}
	var since, at time.Time
	m.StateReport = func(_ context.Context, _ AgentDefinition, s agentstate.State, stamped time.Time) error {
		since, at = s.Since, stamped
		return nil
	}
	m.mu.Lock()
	m.progressPresence(context.Background(), a)
	m.mu.Unlock()
	if at.IsZero() {
		t.Fatal("no report sent")
	}
	if at.Before(since) {
		t.Fatalf("report at %s is before since %s", at, since)
	}
}

func TestDashboardStopReportIsTriedOnceNotRetried(t *testing.T) {
	m, _, _, now := setup(t)
	reports := 0
	m.StateReport = func(context.Context, AgentDefinition, agentstate.State, time.Time) error {
		reports++
		return errors.New("state API returned HTTP 401")
	}
	a := &managed{def: AgentDefinition{AgentID: "agm_removed", Name: "removed", Workstream: "478", State: "stopped-by-dashboard", Reason: "stopped from the dashboard", Desired: "running", Kind: "attached"}, presence: agentstate.New("pi", *now), nextTaskCheck: now.Add(time.Hour)}
	for i := 0; i < 3; i++ {
		*now = now.Add(time.Minute)
		m.mu.Lock()
		m.progressPresence(context.Background(), a)
		m.mu.Unlock()
	}
	if reports != 1 {
		t.Fatalf("sent %d state reports after a dashboard stop, want exactly 1", reports)
	}
}
