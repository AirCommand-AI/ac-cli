package daemon

import (
	"context"
	"strings"
	"testing"
)

func TestDaemonStartDoesNotRequireTmuxOrPi(t *testing.T) {
	s := Service{Home: t.TempDir(), OS: "linux", Aircom: "/opt/aircom", Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "loginctl" {
			return []byte("yes\n"), nil
		}
		if strings.Contains(name, "tmux") || name == "pi" {
			t.Fatalf("eager tool lookup: %s", name)
		}
		return nil, nil
	}}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(SystemdUnit("/opt/aircom", "/usr/bin/tmux", "/opt/pi", "/usr/bin"), "--tmux") || strings.Contains(LaunchdPlist("/opt/aircom", "/usr/bin/tmux", "/opt/pi", "/usr/bin", "/tmp/log"), "--pi") {
		t.Fatal("service still passes eager tool paths")
	}
}
