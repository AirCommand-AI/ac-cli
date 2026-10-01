package supervisor

import (
	"context"
	"io"
	"sync"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
	"os"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestHeadlessPushPollRaceSendsOnce(t *testing.T) {
	ctx := context.Background()
	m, _, poll, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	f := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return f }
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
	if err := listenstore.NewStore(m.Home).SaveCursor(d.AgentID, cred.WorkstreamKey(), "c0"); err != nil {
		t.Fatal(err)
	}
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "agm_lead", SenderNature: "agent"}
	poll.feed = Feed{Notifications: []Notification{n}, Cursor: "c1"}
	var wg sync.WaitGroup
	for _, op := range []func() error{func() error { return m.Wake(ctx, d.AgentID, n) }, func() error { return m.CatchUp(ctx) }} {
		wg.Add(1)
		go func(op func() error) {
			defer wg.Done()
			if err := op(); err != nil {
				t.Error(err)
			}
		}(op)
	}
	wg.Wait()
	if len(f.Sent) != 2 || f.Sent[1].Source != n.MessageID {
		t.Fatalf("expected startup plus one wake: %+v", f.Sent)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
}

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
