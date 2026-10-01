package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type fakeSupervisor struct {
	mu        sync.Mutex
	calls     []string
	ready     chan struct{}
	runErr    error
	exitEarly bool
}

func (f *fakeSupervisor) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}
func (f *fakeSupervisor) Start(_ context.Context, d Definition) error {
	f.record("start:" + d.Name)
	return nil
}
func (f *fakeSupervisor) Driver(string) (pidriver.Driver, bool) { return nil, false }
func (f *fakeSupervisor) Takeover(_ context.Context, name string) (supervisor.TakeoverSpec, error) {
	f.record("takeover:" + name)
	return supervisor.TakeoverSpec{PiPath: "pi", SessionID: "agm_1"}, nil
}
func (f *fakeSupervisor) ResumeTakeover(name string) error { f.record("resume:" + name); return nil }
func (f *fakeSupervisor) Subscribe(string) (<-chan pidriver.Event, func(), bool) {
	return nil, nil, false
}
func (f *fakeSupervisor) History(string, string, int) ([]json.RawMessage, string, error) {
	return nil, "", nil
}
func (f *fakeSupervisor) Mode(_ context.Context, name, mode string) error {
	f.record("mode:" + name + ":" + mode)
	return nil
}
func (f *fakeSupervisor) Stop(_ context.Context, name string) error {
	f.record("stop:" + name)
	return nil
}
func (f *fakeSupervisor) Remove(_ context.Context, name string) error {
	f.record("remove:" + name)
	return nil
}
func (f *fakeSupervisor) List(context.Context) ([]Status, error) {
	return []Status{{Name: "eng-1", AgentID: "agm_1", State: "running"}}, nil
}
func (f *fakeSupervisor) Shutdown(_ context.Context, stop bool) error {
	if stop {
		f.record("shutdown:agents")
	} else {
		f.record("shutdown:leave")
	}
	return nil
}
func (f *fakeSupervisor) Run(ctx context.Context) error {
	if f.exitEarly {
		return f.runErr
	}
	<-ctx.Done()
	return nil
}

// macOS test temp directories under /var/folders can exceed sockaddr_un's
// 103-byte pathname limit once the daemon's storage suffix is appended.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "acd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func TestControlAndLock(t *testing.T) {
	home := shortHome(t)
	f := &fakeSupervisor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, home, f) }()
	socket := storagepath.DaemonSocket(home)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	if stat, err := os.Stat(socket); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("socket %v %v", stat, err)
	}
	if err := Serve(ctx, home, &fakeSupervisor{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second daemon: %v", err)
	}
	for _, req := range []Request{{Op: "status"}, {Op: "agent.start", Name: "eng-1", AgentID: "agm_1", Organization: "Air Command", Workstream: "626", WorkFolder: "/tmp/work"}, {Op: "agent.stop", Name: "eng-1"}, {Op: "agent.remove", Name: "eng-1"}, {Op: "agent.list"}} {
		data, err := Call(ctx, home, req)
		if err != nil {
			t.Fatal(err)
		}
		if req.Op == "status" && !strings.Contains(string(data), `"startedAt"`) {
			t.Fatal(string(data))
		}
	}
	if _, err := Call(ctx, home, Request{Op: "agent.start"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid: %v", err)
	}
	// The shutdown response must be delivered before the listener exits.
	if _, err := Call(ctx, home, Request{Op: "shutdown", StopAgents: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.calls, ","); got != "start:eng-1,stop:eng-1,remove:eng-1,shutdown:agents" {
		t.Fatal(got)
	}
	if _, err := os.Stat(storagepath.DaemonLog(home)); err != nil {
		t.Fatal(err)
	}
}
func TestLongSocketPathRejectedBeforeBindOrDial(t *testing.T) {
	home := filepath.Join(shortHome(t), strings.Repeat("a", 100))
	for _, err := range []error{Serve(context.Background(), home, &fakeSupervisor{}), func() error { _, err := Call(context.Background(), home, Request{Op: "status"}); return err }()} {
		if err == nil || !strings.Contains(err.Error(), "socket path is too long") {
			t.Fatalf("socket path error = %v", err)
		}
	}
}
func TestUnexpectedSupervisorExitFailsDaemon(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"returned_error", errors.New("boom")}, {"returned_nil", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			err := Serve(context.Background(), shortHome(t), &fakeSupervisor{exitEarly: true, runErr: tc.err})
			if err == nil {
				t.Fatal("daemon exited successfully when supervisor failed")
			}
		})
	}
}
func TestInvalidJSON(t *testing.T) {
	f := &fakeSupervisor{}
	a, b := net.Pipe()
	defer b.Close()
	go serveConnection(context.Background(), a, f, time.Now(), "test", "log", 1, func() {}, &atomic.Bool{}, nil)
	_, _ = b.Write([]byte("{invalid}\n"))
	var response Response
	if err := json.NewDecoder(b).Decode(&response); err != nil || response.Error.Code != "invalid" {
		t.Fatalf("%+v %v", response, err)
	}
}
func TestServiceGoldens(t *testing.T) {
	cases := []struct{ name, got string }{{"systemd", SystemdUnit("/opt/aircom", "/usr/bin/tmux", "/opt/pi", "/usr/bin:/opt")}, {"launchd", LaunchdPlist("/opt/aircom", "/usr/bin/tmux", "/opt/pi", "/usr/bin:/opt", "/home/a/.aircommand/daemon/daemon.log")}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.got != string(want) {
				t.Fatalf("%s service definition differs from golden:\n%s", tc.name, tc.got)
			}
		})
	}
}
