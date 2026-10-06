package supervisor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestAttachedClaimPersistenceLivenessAndIsolation(t *testing.T) {
	m, tm, poll, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_attached", WorkstreamCode: "478", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_attached"}); err != nil {
		t.Fatal(err)
	}
	claim, err := m.Claim("agm_attached", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Claim("agm_attached", "478", os.Getpid()); err == nil {
		t.Fatal("second claim admitted")
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_attached", Name: "person", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: takeoverStartTime(os.Getpid()), SessionID: "sess"}); err != nil {
		t.Fatal(err)
	}
	m.ReleaseClaim(claim)
	if !agentlock.Held(m.Home, "agm_attached") {
		t.Fatal("attached lock released")
	}
	if defs := m.Definitions(); len(defs) != 0 {
		t.Fatalf("attached leaked into definitions: %+v", defs)
	}
	if err = m.PostDesired(context.Background(), "person"); err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 0 || tm.kills != 0 {
		t.Fatal("person's pi launched or killed")
	}
	if poll.calls != 1 {
		t.Fatalf("poll calls = %d", poll.calls)
	}
	if err = m.SessionEvent("agm_attached", "working", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = listenstore.NewStore(m.Home).AppendNotification("agm_attached", map[string]string{"summary": "pointer"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.SpoolPath("agm_attached"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SessionAck("agm_attached", info.Size()+1); err == nil {
		t.Fatal("accepted offset beyond spool")
	}
	if err = m.SessionAck("agm_attached", info.Size()); err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(context.Background(), "person"); err != nil {
		t.Fatal(err)
	}
	if tm.kills != 0 || !agentlock.Held(m.Home, "agm_attached") {
		t.Fatal("stop killed person's process or released lock")
	}
	m2, tm2, _, _ := setup(t)
	m2.Home = m.Home
	m.release(m.agents["person"]) // Simulate daemon exit, not removing the persisted record.
	m2.mu.Lock()
	err = m2.boot(context.Background())
	m2.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !agentlock.Held(m2.Home, "agm_attached") {
		t.Fatal("boot did not relock")
	}
	if err = m2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tm2.launches) != 0 || tm2.kills != 0 {
		t.Fatal("boot launched or killed attached session")
	}
	if got, ok := m2.SessionLookup("sess"); !ok || got.Offset != info.Size() {
		t.Fatalf("boot lost session/offset: %+v %v", got, ok)
	}
	if err = m2.Remove(context.Background(), "person"); err != nil {
		t.Fatal(err)
	}
	if agentlock.Held(m2.Home, "agm_attached") {
		t.Fatal("remove did not release lock")
	}
}
func TestAttachedPlacementWaitJoinsWithoutStartingPi(t *testing.T) {
	m, tm, poll, _ := setup(t)
	claim, err := m.Claim("agm_wait", "", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_wait", Name: "waiting", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), SessionID: "wait-id"}); err != nil {
		t.Fatal(err)
	}
	assigned := false
	calls := 0
	m.PlaceAttached = func(_ context.Context, d AgentDefinition) (string, string, error) {
		calls++
		if !assigned {
			return "", "", nil
		}
		if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: "478", OrganizationID: "org_a", APIToken: "token", SocketAddress: "ac:agm_wait"}); err != nil {
			return "", "", err
		}
		return "org_a", "478", nil
	}
	if err = m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || poll.calls != 0 {
		t.Fatalf("unplaced poll calls %d, server polls %d", calls, poll.calls)
	}
	assigned = true
	m.mu.Lock()
	m.agents["waiting"].nextPlacement = time.Time{}
	m.mu.Unlock()
	if err = m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d, ok := m.Attached("agm_wait"); !ok || d.Workstream != "478" || d.Organization != "org_a" {
		t.Fatalf("placement not saved: %+v", d)
	}
	if poll.calls != 1 || len(tm.launches) != 0 {
		t.Fatalf("placement poll %d launches %d", poll.calls, len(tm.launches))
	}
	m.ReleaseClaim(claim)
	if err := m.Remove(context.Background(), "waiting"); err != nil {
		t.Fatal(err)
	}
}
func TestMachineStopDoesNotKillOrUnlockAttachedSession(t *testing.T) {
	m, tm, _, _ := setup(t)
	claim, err := m.Claim("agm_machine", "", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_machine", Name: "person", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), Program: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err = m.SetMachineState(context.Background(), "stopping"); err != nil {
		t.Fatal(err)
	}
	if tm.kills != 0 || !agentlock.Held(m.Home, "agm_machine") {
		t.Fatal("machine stop touched attached process or lock")
	}
	if !m.AgentsStopped() {
		t.Fatal("attached agent counted against started agents stopped")
	}
	m.ReleaseClaim(claim)
}
func TestAttachedDashboardStopNeverSignalsPersonPi(t *testing.T) {
	m, tm, poll, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_stop", WorkstreamCode: "478", APIToken: "token", SocketAddress: "ac:agm_stop"}); err != nil {
		t.Fatal(err)
	}
	claim, err := m.Claim("agm_stop", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_stop", Name: "person", Workstream: "478", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), Program: "pi"}); err != nil {
		t.Fatal(err)
	}
	poll.err = ErrAgentStopped
	if err = m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tm.kills != 0 || len(tm.launches) != 0 {
		t.Fatal("dashboard stop signalled person's pi")
	}
	if d, _ := m.Attached("agm_stop"); d.State != "stopped-by-dashboard" || d.Reason != "stopped from the dashboard" {
		t.Fatalf("state: %+v", d)
	}
	if !agentlock.Held(m.Home, "agm_stop") {
		t.Fatal("dashboard stop released lock")
	}
	m.ReleaseClaim(claim)
}
func TestClaimReleasedWithoutAttachment(t *testing.T) {
	m, _, _, _ := setup(t)
	c, err := m.Claim("agm_unplaced", "", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseClaim(c)
	if agentlock.Held(m.Home, "agm_unplaced") {
		t.Fatal("claim lock leaked")
	}
}

func TestNewAttachmentStartsAtEndOfExistingHistory(t *testing.T) {
	m, _, _, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_history", WorkstreamCode: "478", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_history"}); err != nil {
		t.Fatal(err)
	}
	for _, summary := range []string{"old wake in 610", "old wake in 478"} {
		if err := listenstore.NewStore(m.Home).AppendNotification("agm_history", map[string]string{"summary": summary}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(m.SpoolPath("agm_history"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.Claim("agm_history", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_history", Name: "historian", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	m.ReleaseClaim(claim)
	def, ok := m.Attached("agm_history")
	if !ok {
		t.Fatal("not attached")
	}
	if def.Offset != info.Size() {
		t.Fatalf("new attachment offset = %d, want end of history %d (no stale replay)", def.Offset, info.Size())
	}
	// Re-attaching the same session keeps its position.
	if err = m.SessionAck("agm_history", info.Size()); err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(nil, Attachment{AgentID: "agm_history", Name: "historian", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid()), SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if def, _ = m.Attached("agm_history"); def.Offset != info.Size() {
		t.Fatalf("re-attach offset = %d, want %d", def.Offset, info.Size())
	}
}

func TestClaimAllowsTheHoldingSessionToJoinAgain(t *testing.T) {
	m, _, _, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_again", WorkstreamCode: "478", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_again"}); err != nil {
		t.Fatal(err)
	}
	claim, err := m.Claim("agm_again", "478", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_again", Name: "again", Workstream: "478", Program: "other", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid())}); err != nil {
		t.Fatal(err)
	}
	m.ReleaseClaim(claim)
	tests := []struct {
		name string
		peer int
		want bool
	}{
		{name: "same session joins again", peer: os.Getpid(), want: true},
		{name: "unrelated process is refused", peer: os.Getppid(), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			again, err := m.Claim("agm_again", "478", test.peer)
			if got := err == nil; got != test.want {
				t.Fatalf("claim from %d admitted=%v, want %v (err %v)", test.peer, got, test.want, err)
			}
			if again != nil {
				m.ReleaseClaim(again)
			}
		})
	}
}

func TestSameSessionRejoinKeepsRunningListener(t *testing.T) {
	m, _, _, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_rejoin", WorkstreamCode: "478", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_rejoin"}); err != nil {
		t.Fatal(err)
	}
	attach := func() {
		t.Helper()
		claim, err := m.Claim("agm_rejoin", "478", os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		defer m.ReleaseClaim(claim)
		if err = m.Attach(claim, Attachment{AgentID: "agm_rejoin", Name: "rejoin", Workstream: "478", Program: "other", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid())}); err != nil {
			t.Fatal(err)
		}
	}
	attach()
	if def, _ := m.Attached("agm_rejoin"); def.State != "stopped" || def.Reason != "no listener" {
		t.Fatalf("before listener: state %q reason %q", def.State, def.Reason)
	}
	if err := m.SessionConnected("agm_rejoin"); err != nil {
		t.Fatal(err)
	}
	attach()
	if def, _ := m.Attached("agm_rejoin"); def.State != "running" {
		t.Fatalf("same-session re-join reset a running listener to %q (%s)", def.State, def.Reason)
	}
}

// An attached agent that left its workstream (credential gone) must neither
// block the machine's catch-up for other agents nor stay "running".
func TestLeftAttachedAgentDoesNotBlockCatchUp(t *testing.T) {
	m, _, _, _ := setup(t)
	if err := credentials.NewStore(m.Home).Save(credentials.Credential{AgentID: "agm_left", WorkstreamCode: "529", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_left"}); err != nil {
		t.Fatal(err)
	}
	claim, err := m.Claim("agm_left", "529", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Attach(claim, Attachment{AgentID: "agm_left", Name: "leaver", Workstream: "529", Program: "pi", SessionPID: os.Getpid(), SessionStart: SessionProcessStart(os.Getpid())}); err != nil {
		t.Fatal(err)
	}
	m.ReleaseClaim(claim)
	if err := credentials.NewStore(m.Home).Delete("agm_left"); err != nil {
		t.Fatal(err)
	}
	if err := m.CatchUp(context.Background()); err != nil {
		t.Fatalf("catch-up failed because of a left agent: %v", err)
	}
	if def, _ := m.Attached("agm_left"); def.State != "stopped" || def.Reason != "left the workstream" {
		t.Fatalf("left agent state %q reason %q", def.State, def.Reason)
	}
}
