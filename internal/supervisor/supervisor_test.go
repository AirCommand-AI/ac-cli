package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

type fakeTmux struct {
	panes      map[string]Pane
	launches   [][]string
	kills      int
	inspectErr map[string]error
	inspected  map[string]int
}

func (f *fakeTmux) Inspect(_ context.Context, n string) (Pane, error) {
	if f.inspected != nil {
		f.inspected[n]++
	}
	if err := f.inspectErr[n]; err != nil {
		return Pane{}, err
	}
	return f.panes[n], nil
}
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
func TestBootSkipsInvalidAndDuplicateDefinitions(t *testing.T) {
	ctx := context.Background()
	m, tm, _, _ := setup(t)
	good := definition(m.Home)
	good.Name = "original"
	good.Desired = "running"
	if err := atomicJSON(m.definitionPath(good.AgentID), good); err != nil {
		t.Fatal(err)
	}
	duplicate := good
	duplicate.AgentID = "agm_2"
	if err := atomicJSON(m.definitionPath(duplicate.AgentID), duplicate); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(m.Home, ".aircommand", "agents", "agm_3", "daemon.json")
	if err := os.MkdirAll(filepath.Dir(invalid), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	following := good
	following.Name = "following"
	following.AgentID = "agm_4"
	if err := atomicJSON(m.definitionPath(following.AgentID), following); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	m.mu.Lock()
	err := m.boot(ctx)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.agents) != 2 || len(tm.launches) != 2 {
		t.Fatalf("boot failed to continue: %v, %v", m.agents, tm.launches)
	}
	if _, err := os.Stat(invalid); err != nil {
		t.Fatal("invalid definition was modified", err)
	}
	if !strings.Contains(logs.String(), "duplicate agent name") || !strings.Contains(logs.String(), "agm_3") {
		t.Fatal("missing skip diagnostics", logs.String())
	}
	_ = m.Shutdown(ctx, false)
}
func TestBootPreservesCrashBackoff(t *testing.T) {
	ctx := context.Background()
	m, tm, _, now := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	tm.panes[d.Name] = Pane{Exists: true, Dead: true, ExitCode: 7}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(ctx, false); err != nil {
		t.Fatal(err)
	}
	other := New(m.Home, "/bin/pi", "/bin/aircom", tm, nil)
	other.Now = m.Now
	other.mu.Lock()
	err := other.boot(ctx)
	other.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(other.agents[d.Name].def.Crashes) != 1 || len(tm.launches) != 1 {
		t.Fatal("boot recounted crash or restarted early")
	}
	*now = now.Add(5 * time.Second)
	if err := other.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 2 || !contains(tm.launches[1], "--continue") {
		t.Fatal("boot did not resume after backoff")
	}
	_ = other.Shutdown(ctx, false)
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
func TestParseTmuxLiveAndDeadPane(t *testing.T) {
	for _, tc := range []struct {
		line string
		dead bool
		code int
		pid  int
	}{{"0::1234\n", false, 0, 1234}, {"1:17:1234\n", true, 17, 1234}} {
		p, err := parsePaneOutput(tc.line)
		if err != nil || !p.Exists || p.Dead != tc.dead || p.ExitCode != tc.code || p.PID != tc.pid {
			t.Fatalf("parse %q: %+v %v", tc.line, p, err)
		}
	}
	for _, line := range []string{"", "0:broken:0", "1::1234", "0::not-a-pid"} {
		if _, err := parsePaneOutput(line); err == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}
func TestTickRetriesOneAgentWithoutStoppingOthers(t *testing.T) {
	ctx := context.Background()
	m, tm, _, now := setup(t)
	m.Poll = nil
	a := definition(m.Home)
	b := a
	b.Name = "eng-2"
	b.AgentID = "agm_2"
	for _, d := range []AgentDefinition{a, b} {
		if err := m.Start(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	tm.inspectErr = map[string]error{a.Name: errors.New("temporary tmux failure")}
	tm.inspected = map[string]int{}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tm.inspected[b.Name] != 1 {
		t.Fatal("healthy agent not inspected")
	}
	if tm.inspected[a.Name] != 1 {
		t.Fatal("failing agent not inspected")
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tm.inspected[a.Name] != 1 {
		t.Fatal("failing agent retried without backoff")
	}
	delete(tm.inspectErr, a.Name)
	*now = now.Add(5 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tm.inspected[a.Name] != 2 {
		t.Fatal("failing agent not retried")
	}
}
func TestCommandTmuxLivePane(t *testing.T) {
	if _, err := os.Stat("/usr/bin/tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	tm := CommandTmux{Path: "/usr/bin/tmux"}
	name := "supervisor_live_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	defer tm.Kill(ctx, name)
	if err := tm.Start(ctx, AgentDefinition{Name: name, WorkFolder: t.TempDir()}, []string{"/bin/sleep", "10"}); err != nil {
		t.Fatal(err)
	}
	p, err := tm.Inspect(ctx, name)
	if err != nil || !p.Exists || p.Dead || p.PID <= 0 {
		t.Fatalf("live pane %+v %v", p, err)
	}
}
func TestCommandTmuxDeadPane(t *testing.T) {
	if _, err := os.Stat("/usr/bin/tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	tm := CommandTmux{Path: "/usr/bin/tmux"}
	name := "supervisor_test_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	d := AgentDefinition{Name: name, WorkFolder: t.TempDir()}
	defer tm.Kill(ctx, name)
	if err := tm.Start(ctx, d, []string{"/bin/true"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, err := tm.Inspect(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Exists && p.Dead {
			if p.ExitCode != 0 {
				t.Fatalf("exit code %d", p.ExitCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead pane not retained")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
