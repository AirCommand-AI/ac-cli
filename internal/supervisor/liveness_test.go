package supervisor

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestToolOf(t *testing.T) {
	cases := []struct {
		name          string
		data          map[string]any
		tool, command string
	}{
		{"pi bash", map[string]any{"toolName": "bash", "args": map[string]any{"command": "cargo  test\n --all"}}, "bash", "cargo test --all"},
		{"claude Bash", map[string]any{"toolName": "Bash", "args": map[string]any{"command": "npm install"}}, "Bash", "npm install"},
		{"other tool", map[string]any{"toolName": "read", "args": map[string]any{"path": "/etc/secret"}}, "read", ""},
		{"long", map[string]any{"toolName": "bash", "args": map[string]any{"command": strings.Repeat("x", 200)}}, "bash", strings.Repeat("x", 79) + "…"},
		{"none", map[string]any{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, command := toolOf(tc.data)
			if tool != tc.tool || command != tc.command {
				t.Fatalf("toolOf = %q %q", tool, command)
			}
		})
	}
}

func TestPSTime(t *testing.T) {
	for in, want := range map[string]uint64{"0:01.50": 150, "1:00.00": 6000, "1-00:00:00": 8640000} {
		if got := psTime(in); got != want {
			t.Fatalf("psTime(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestToolCountersSeeChildren(t *testing.T) {
	cmd := exec.Command("sh", "-c", "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done; sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if _, _, ok := toolCounters(os.Getpid()); !ok {
		t.Fatal("no counters for a running child")
	}
	if _, _, ok := toolCounters(0); ok {
		t.Fatal("counters for no process")
	}
}

func TestDiskFullReadsFilesystem(t *testing.T) {
	if _, ok := diskFull(t.TempDir()); !ok {
		t.Fatal("statfs failed")
	}
}
