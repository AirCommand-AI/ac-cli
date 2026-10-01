package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

func TestDaemonBootContinuesWhenExtensionCannotBeSynced(t *testing.T) {
	home := shortHome(t)
	// A regular file blocking the extensions directory fails on every OS,
	// including tests run as root where chmod-based failures are unreliable.
	blocker := filepath.Join(home, ".pi", "agent", "extensions")
	if err := os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	started := false
	stop := errors.New("supervisor started")
	c := Commands{Home: home, NewSupervisor: func(_, _ string) (Supervisor, error) {
		started = true
		return &fakeSupervisor{exitEarly: true, runErr: stop}, nil
	}}
	err := c.RunDaemon([]string{"run", "--tmux", "/bin/tmux", "--pi", "/bin/pi"})
	if !started || !errors.Is(err, stop) {
		t.Fatalf("supervisor started=%v, daemon error=%v", started, err)
	}
	data, err := os.ReadFile(storagepath.DaemonLog(home))
	if err != nil || !strings.Contains(string(data), "warning: sync pi extension at daemon boot") {
		t.Fatalf("daemon warning: %s, %v", data, err)
	}
}
