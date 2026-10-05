package app

import (
	"errors"
	"testing"
)

func TestDiscoverSessionWalksAncestorProcessTable(t *testing.T) {
	table := map[int]struct {
		parent     int
		cmd, start string
	}{99: {88, "aircom join --agent x", "1"}, 88: {77, "/bin/bash -c aircom join --agent claude --workstream 478", "2"}, 77: {1, "node /tmp/@earendil-works/pi-coding-agent/dist/cli.js", "first"}}
	snapshot := func(pid int) (int, string, string, error) {
		row, ok := table[pid]
		if !ok {
			return 0, "", "", errors.New("unknown pid")
		}
		return row.parent, row.cmd, row.start, nil
	}
	session, err := discoverSession(99, snapshot)
	if err != nil || session.SessionPID != 77 || session.SessionStart != "first" || session.Program != "pi" {
		t.Fatalf("session: %+v %v", session, err)
	}
}
func TestDiscoverSessionAcceptsPiProcessTitle(t *testing.T) {
	for _, cmd := range []string{"pi", "pi --continue"} {
		t.Run(cmd, func(t *testing.T) {
			session, err := discoverSession(99, func(pid int) (int, string, string, error) { return 1, cmd, "start", nil })
			if err != nil || session.Program != "pi" || session.SessionPID != 99 {
				t.Fatalf("session: %+v %v", session, err)
			}
		})
	}
}
func TestDiscoverSessionRefusesStandaloneShell(t *testing.T) {
	_, err := discoverSession(99, func(pid int) (int, string, string, error) { return 1, "/bin/bash", "start", nil })
	if err == nil {
		t.Fatal("standalone command registered a session")
	}
}
