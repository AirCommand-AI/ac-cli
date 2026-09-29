package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

type fakeTmux struct {
	panes    map[string]Pane
	launches [][]string
	kills    int
}

func (f *fakeTmux) Inspect(_ context.Context, n string) (Pane, error) { return f.panes[n], nil }
func (f *fakeTmux) Start(_ context.Context, d AgentDefinition, args []string) error {
	f.panes[d.Name] = Pane{Exists: true, PID: 123}
	f.launches = append(f.launches, args)
	return nil
}
func (f *fakeTmux) Kill(_ context.Context, n string) error { delete(f.panes, n); f.kills++; return nil }

type fakePoll struct {
	feed  Feed
	err   error
	calls int
}

func (p *fakePoll) Fetch(_ context.Context, _ AgentDefinition, _ string, _ bool) (Feed, error) {
	p.calls++
	return p.feed, p.err
}
func (p *fakePoll) Spool(_ context.Context, _ AgentDefinition, n Notification) (any, error) {
	return map[string]string{"messageId": n.MessageID, "senderId": n.SenderID, "summary": "pointer"}, nil
}
func setup(t *testing.T) (*Manager, *fakeTmux, *fakePoll, *time.Time) {
	t.Helper()
	home := t.TempDir()
	tm := &fakeTmux{panes: map[string]Pane{}}
	poll := &fakePoll{feed: Feed{Cursor: "c1", PollAfter: time.Second}}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	m := New(home, "/bin/pi", "/bin/aircom", tm, poll)
	m.Now = func() time.Time { return now }
	return m, tm, poll, &now
}
func definition(home string) AgentDefinition {
	return AgentDefinition{AgentID: "agm_1", Name: "eng-1", Organization: "Air Command", Workstream: "626", WorkFolder: filepath.Join(home, "work"), Repos: []string{"org/repo"}}
}
func TestRestartCutoffAndExplicitFreshStart(t *testing.T) {
	ctx := context.Background()
	m, tm, _, now := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 1 || contains(tm.launches[0], "--continue") {
		t.Fatal("fresh start must not continue")
	}
	for i := 1; i <= 5; i++ {
		tm.panes[d.Name] = Pane{Exists: true, Dead: true, ExitCode: 17}
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		state := m.agents[d.Name].def.State
		if i == 5 {
			if state != "crashed" {
				t.Fatalf("cutoff state %s", state)
			}
			break
		}
		if state != "starting" {
			t.Fatalf("crash %d: %s", i, state)
		}
		*now = now.Add(backoff(i) - time.Millisecond)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if len(tm.launches) != i {
			t.Fatal("restarted before backoff")
		}
		*now = now.Add(time.Millisecond)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if len(tm.launches) != i+1 || !contains(tm.launches[i], "--continue") {
			t.Fatal("crash restart must continue")
		}
	}
	*now = now.Add(20 * time.Minute)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 5 {
		t.Fatal("crashed agent restarted")
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if m.agents[d.Name].def.State != "running" || len(m.agents[d.Name].def.Crashes) != 0 {
		t.Fatal("start did not reset crash state")
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if contains(tm.launches[len(tm.launches)-1], "--continue") {
		t.Fatal("explicit start after stop continued")
	}
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func TestBootReadoptAndShutdown(t *testing.T) {
	ctx := context.Background()
	m, tm, _, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(ctx, false); err != nil {
		t.Fatal(err)
	}
	if tm.kills != 1 {
		t.Fatal("shutdown(false) killed pi")
	} // first kill is preparation for first start
	other := New(m.Home, "/bin/pi", "/bin/aircom", tm, nil)
	other.Now = m.Now
	other.mu.Lock()
	err := other.boot(ctx)
	other.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 1 || other.agents[d.Name].def.State != "running" {
		t.Fatal("did not re-adopt existing pane")
	}
	if err := other.Shutdown(ctx, true); err != nil {
		t.Fatal(err)
	}
	var saved AgentDefinition
	bytes, err := os.ReadFile(other.definitionPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bytes, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Desired != "running" || saved.State != "stopped" {
		t.Fatal("shutdown must keep desired state")
	}
	third := New(m.Home, "/bin/pi", "/bin/aircom", tm, nil)
	third.mu.Lock()
	err = third.boot(ctx)
	third.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 2 {
		t.Fatal("boot did not restart desired running")
	}
	_ = third.Shutdown(ctx, false)
}
func TestPollingDedupePersistenceAndDashboardStop(t *testing.T) {
	ctx := context.Background()
	m, tm, p, now := setup(t)
	d := definition(m.Home)
	cred := credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, OrganizationID: "org_1", APIToken: "secret", SocketKey: "socket", SocketAddress: "ac:agm_1"}
	if err := credentials.NewStore(m.Home).Save(cred); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "agm_sender", SenderNature: "agent", At: "now"}
	p.feed.Notifications = []Notification{n}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	} // no cursor: baseline
	*now = now.Add(time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	p.feed.Cursor = "c2"
	*now = now.Add(time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(listenstore.NewStore(m.Home).SpoolPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(bytes), "messageId") != 1 || !strings.Contains(string(bytes), "agm_sender") {
		t.Fatal("missing or duplicate pointer", string(bytes))
	}
	if err := m.Shutdown(ctx, false); err != nil {
		t.Fatal(err)
	}
	other := New(m.Home, "/bin/pi", "/bin/aircom", tm, p)
	other.Now = m.Now
	other.mu.Lock()
	err = other.boot(ctx)
	other.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err := other.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	bytes, _ = os.ReadFile(listenstore.NewStore(m.Home).SpoolPath(d.AgentID))
	if strings.Count(string(bytes), "messageId") != 1 {
		t.Fatal("dedupe lost across restart")
	}
	p.err = ErrAgentStopped
	*now = now.Add(time.Second)
	if err := other.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if other.agents[d.Name].def.State != "stopped-by-dashboard" || tm.panes[d.Name].Exists {
		t.Fatal("terminal error did not stop pi")
	}
	p.err = nil
	*now = now.Add(time.Minute)
	if err := other.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tm.panes[d.Name].Exists {
		t.Fatal("dashboard-stopped agent restarted")
	}
	_ = other.Shutdown(ctx, false)
}
func TestInvalidNameAndLockedAgent(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Name = "bad:name"
	if err := m.Start(ctx, d); err == nil {
		t.Fatal("accepted tmux target injection")
	}
	d.Name = "eng-1"
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	other := New(m.Home, "/bin/pi", "/bin/aircom", &fakeTmux{panes: map[string]Pane{}}, nil)
	if err := other.Start(ctx, d); err == nil {
		t.Fatal("accepted already locked agent")
	}
	_ = m.Shutdown(ctx, false)
	if errors.Is(nil, ErrAgentStopped) {
		t.Fatal("unexpected")
	}
}
