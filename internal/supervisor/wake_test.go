package supervisor

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestPushCatchUpAndRestartDedupe(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	store := credentials.NewStore(m.Home)
	if err := store.Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, APIToken: "agent-token", SocketKey: "key", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	cred, err := store.FindByAgent(d.Workstream, d.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	listener := listenstore.NewStore(m.Home)
	if err := listener.SaveCursor(d.AgentID, cred.WorkstreamKey(), "c0"); err != nil {
		t.Fatal(err)
	}
	n := agentapi.Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "agm_lead", SenderNature: "agent"}
	poll.feed = Feed{Notifications: []Notification{n}, Cursor: "c1"}
	if err := m.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	cursor, _, err := listener.LoadCursor(d.AgentID, cred.WorkstreamKey())
	if err != nil || cursor != "c0" {
		t.Fatalf("push advanced cursor: %q %v", cursor, err)
	}
	if err := m.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(ctx, false); err != nil {
		t.Fatal(err)
	}
	next := New(m.Home, m.Pi, m.CLI, tm, poll)
	if err := next.boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := next.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	path := listener.SpoolPath(d.AgentID)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "0123456789abcdef"); got != 1 {
		t.Fatalf("duplicate wake count %d: %s", got, body)
	}
}
