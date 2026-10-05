package supervisor

import (
	"context"
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
