package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
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
	feed   Feed
	err    error
	calls  int
	states []string
}

func (p *fakePoll) Fetch(_ context.Context, _ AgentDefinition, _ string, _ bool) (Feed, error) {
	p.calls++
	return p.feed, p.err
}
func (p *fakePoll) State(_ context.Context, _ AgentDefinition, state string) error {
	p.states = append(p.states, state)
	return nil
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
func TestSessionMigrationSkipsExistingFixedSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	work := filepath.Join(root, "work", "eng-1")
	dir := filepath.Join(root, ".pi", "agent", "sessions", "--"+strings.ReplaceAll(strings.TrimPrefix(work, "/"), "/", "-")+"--")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "2026-09-01_legacy.jsonl")
	if err := os.WriteFile(legacy, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := migrationSource(work, "agm_1"); got != legacy {
		t.Fatalf("migration source %q, want %q", got, legacy)
	}
	fixed := filepath.Join(dir, "2026-10-01_agm_1.jsonl")
	if err := os.WriteFile(fixed, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := migrationSource(work, "agm_1"); got != "" {
		t.Fatalf("existing fixed session would be forked again: %q", got)
	}
}

func TestHeadlessDriverStartWakesAndCrashBackoff(t *testing.T) {
	ctx := context.Background()
	m, tm, poll, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	var drivers []*pidriver.Fake
	m.NewDriver = func(io.Writer) pidriver.Driver {
		f := pidriver.NewFake()
		drivers = append(drivers, f)
		return f
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 0 || len(drivers) != 1 || len(drivers[0].Launches) != 1 || drivers[0].Launches[0].SessionID != d.AgentID {
		t.Fatal("headless must start the RPC driver, not tmux")
	}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 {
		t.Fatal("restarting a running headless agent spawned a second pi")
	}
	if len(drivers[0].Sent) != 1 || drivers[0].Sent[0].Source != "startup" {
		t.Fatal("missing first startup prompt")
	}
	drivers[0].MarkReady()
	drivers[0].EventCh <- pidriver.Event{Kind: "agent_start"}
	drivers[0].EventCh <- pidriver.Event{Kind: "agent_settled"}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(poll.states) != 2 || poll.states[0] != "working" || poll.states[1] != "idle" {
		t.Fatalf("pi work state: %v", poll.states)
	}
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "ac_sender", SenderNature: "human", Priority: "urgent"}
	if err := m.Wake(ctx, d.AgentID, n); err != nil {
		t.Fatal(err)
	}
	if len(drivers[0].Sent) != 2 || drivers[0].Sent[1].Kind != pidriver.Urgent {
		t.Fatal("urgent wake did not reach headless driver")
	}
	m.agents[d.Name].nextRetry = time.Time{}
	drivers[0].ExitCh <- pidriver.Exit{Code: 7}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || m.agents[d.Name].def.State != "starting" {
		t.Fatal("unexpected crash restart")
	}
	*now = now.Add(5 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 2 || drivers[1].Launches[0].SessionID != d.AgentID {
		t.Fatal("headless crash did not resume the same session")
	}
}

func TestStartPreservesModeAndDurableFields(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	a := m.agents[d.Name]
	a.def.Nudge = &NudgeState{TaskID: "task1", NudgedAt: "2026-10-01T00:00:00Z"}
	a.def.SessionMigrated = true
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if a.def.Nudge == nil || a.def.Nudge.TaskID != "task1" || !a.def.SessionMigrated || a.def.Mode != "tmux" {
		t.Fatalf("start dropped durable fields: %+v", a.def)
	}
	if err := m.Stop(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Mode(ctx, d.Name, "headless"); err != nil {
		t.Fatal(err)
	}
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if a.def.Mode != "headless" || a.def.Nudge == nil || !a.def.SessionMigrated {
		t.Fatalf("start lost mode or fields: %+v", a.def)
	}
}

func TestModeRequiresStoppedAgentAndPersists(t *testing.T) {
	m, _, _, _ := setup(t)
	def := definition(m.Home)
	if err := m.Start(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if err := m.Mode(context.Background(), def.Name, "headless"); err == nil {
		t.Fatal("running agent changed mode")
	}
	if err := m.Stop(context.Background(), def.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Mode(context.Background(), def.Name, "headless"); err != nil {
		t.Fatal(err)
	}
	var saved AgentDefinition
	data, err := os.ReadFile(m.definitionPath(def.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Mode != "headless" {
		t.Fatalf("saved mode = %q", saved.Mode)
	}
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
		if len(tm.launches) != i+1 || !contains(tm.launches[i], "--session-id") {
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
	if len(tm.launches) != 2 || !contains(tm.launches[1], "--session-id") {
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
		line   string
		dead   bool
		code   int
		pid    int
		signal string
	}{{"0:::1234\n", false, 0, 1234, ""}, {"1:17::1234\n", true, 17, 1234, ""}, {"1::15:1234\n", true, -1, 1234, "15"}, {"1:::1234\n", true, -1, 1234, "unknown"}} {
		p, err := parsePaneOutput(tc.line)
		if err != nil || !p.Exists || p.Dead != tc.dead || p.ExitCode != tc.code || p.PID != tc.pid || p.Signal != tc.signal {
			t.Fatalf("parse %q: %+v %v", tc.line, p, err)
		}
	}
	for _, line := range []string{"", "0:broken:0", "1::1234", "0:::not-a-pid", "1:broken:15:1234"} {
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

type recordingCommandTmux struct {
	CommandTmux
	launches [][]string
}

func (t *recordingCommandTmux) Start(ctx context.Context, d AgentDefinition, args []string) error {
	t.launches = append(t.launches, append([]string(nil), args...))
	return t.CommandTmux.Start(ctx, d, args)
}
func TestSignalKilledPaneRestartsWithContinue(t *testing.T) {
	if _, err := os.Stat("/usr/bin/tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	home := t.TempDir()
	tm := &recordingCommandTmux{CommandTmux: CommandTmux{Path: "/usr/bin/tmux"}}
	script := filepath.Join(home, "pi-test")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 1000\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m := New(home, script, "/bin/true", tm, nil)
	now := time.Now()
	m.Now = func() time.Time { return now }
	d := definition(home)
	d.Name = "supervisor_signal_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	defer tm.Kill(ctx, d.Name)
	defer m.Shutdown(ctx, false)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	pane, err := tm.Inspect(ctx, d.Name)
	if err != nil || !pane.Exists || pane.Dead {
		t.Fatalf("initial pane %+v %v", pane, err)
	}
	if err := syscall.Kill(pane.PID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pane, err = tm.Inspect(ctx, d.Name)
		if err != nil {
			t.Fatal(err)
		}
		if pane.Dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("signaled pane did not die")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pane.ExitCode != -1 || pane.Signal == "" {
		t.Fatalf("signaled pane %+v", pane)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	last := m.agents[d.Name].def.LastExit
	if last == nil || last.Code != -1 || last.Signal != pane.Signal {
		t.Fatalf("last exit %+v, pane %+v", last, pane)
	}
	now = now.Add(5 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.launches) != 2 || !contains(tm.launches[1], "--session-id") {
		t.Fatalf("restart args: %v", tm.launches)
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
