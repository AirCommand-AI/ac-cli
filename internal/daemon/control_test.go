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

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

type fakeSupervisor struct {
	mu    sync.Mutex
	calls []string
	ready chan struct{}
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
func (f *fakeSupervisor) Run(ctx context.Context) error { <-ctx.Done(); return nil }

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
func TestInvalidJSON(t *testing.T) {
	f := &fakeSupervisor{}
	a, b := net.Pipe()
	defer b.Close()
	go serveConnection(context.Background(), a, f, time.Now(), "log", 1, func() {}, &atomic.Bool{})
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
