package daemon

import (
	"context"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceWritesAndEnablesUnit(t *testing.T) {
	home := t.TempDir()
	var calls []string
	s := Service{Home: home, OS: "linux", Aircom: "/opt/aircom", Tmux: "/usr/bin/tmux", Pi: "/opt/pi", Path: "/usr/bin:/opt", Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "loginctl" {
			return []byte("yes\n"), nil
		}
		return nil, nil
	}}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".config/systemd/user/aircom-daemon.service"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != SystemdUnit("/opt/aircom", "/usr/bin/tmux", "/opt/pi", "/usr/bin:/opt") {
		t.Fatal(string(body))
	}
	if len(calls) != 3 || !strings.Contains(calls[2], "enable --now") {
		t.Fatal(calls)
	}
}
func TestLaunchdEscapesPathStrings(t *testing.T) {
	plist := LaunchdPlist("/opt/a&b", "/opt/t<mux", "/opt/pi", "/opt/a&b", "/tmp/l<g")
	for _, want := range []string{"/opt/a&amp;b", "/opt/t&lt;mux", "/tmp/l&lt;g", "<key>RunAtLoad</key><true/>"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q: %s", want, plist)
		}
	}
}
func TestServiceRequiresLinger(t *testing.T) {
	s := Service{Home: t.TempDir(), OS: "linux", Aircom: "/opt/aircom", Tmux: "/usr/bin/tmux", Pi: "/opt/pi", Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("no"), nil }}
	if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "linger") {
		t.Fatal(err)
	}
}
func TestServiceStopSendsShutdownFirst(t *testing.T) {
	home := shortHome(t)
	f := &fakeSupervisor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, home, f) }()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(storagepath.DaemonSocket(home)); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := Service{Home: home, OS: "linux", Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.calls) != 1 || f.calls[0] != "shutdown:agents" {
			t.Errorf("service manager called before agent shutdown: %v", f.calls)
		}
		return nil, nil
	}}
	if err := s.Stop(context.Background()); err != nil {
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
}
func TestServiceStopWithoutDaemon(t *testing.T) {
	var called bool
	s := Service{Home: shortHome(t), OS: "linux", Run: func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }}
	if err := s.Stop(context.Background()); err != nil || !called {
		t.Fatalf("stop: %v, called=%v", err, called)
	}
}
