package claudedriver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const agentID = "agm_0123456789abcdef0123456789abcdef"

func fake(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	path, err := filepath.Abs("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func start(t *testing.T, config string, env ...string) (*Driver, pidriver.LaunchSpec) {
	t.Helper()
	uuid, err := SessionUUID(agentID)
	if err != nil {
		t.Fatal(err)
	}
	spec := pidriver.LaunchSpec{PiPath: fake(t), WorkDir: t.TempDir(), SessionID: uuid, Args: []string{"--model", "claude-opus-5-5"}, Env: append([]string{"CLAUDE_CONFIG_DIR=" + config, "PATH=" + os.Getenv("PATH")}, env...)}
	d := New(pidriver.Options{Log: os.Stderr}).(*Driver)
	if err := d.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	<-d.Ready()
	return d, spec
}

func waitFor(t *testing.T, d *Driver, kind string) pidriver.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-d.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event", kind)
		}
	}
}

func TestSessionUUID(t *testing.T) {
	got, err := SessionUUID(agentID)
	if err != nil || got != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("SessionUUID = %q %v", got, err)
	}
	if _, err := SessionUUID("agm_short"); err == nil {
		t.Fatal("short ID accepted")
	}
}

func TestTurnsToolsHistoryAndResume(t *testing.T) {
	config := t.TempDir()
	d, spec := start(t, config)
	if err := d.Send(pidriver.Outgoing{Text: "hello", Kind: pidriver.Regular}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, "agent_start")
	waitFor(t, d, "agent_settled")
	if err := d.Send(pidriver.Outgoing{Text: "please use a tool", Kind: pidriver.Urgent}); err != nil {
		t.Fatal(err)
	}
	start := waitFor(t, d, "tool_execution_start")
	if start.Data["toolName"] != "Bash" {
		t.Fatalf("tool start = %+v", start.Data)
	}
	waitFor(t, d, "tool_execution_end")
	waitFor(t, d, "agent_settled")
	if s := d.State(); s.Streaming || s.CurrentTool != "" || !s.Settled {
		t.Fatalf("state after turn = %+v", s)
	}
	entries, next, err := d.History(context.Background(), "", 10)
	if err != nil || len(entries) != 4 || next != "a2" {
		t.Fatalf("history = %d %q %v", len(entries), next, err)
	}
	after, _, _ := d.History(context.Background(), "a1", 10)
	if len(after) != 2 {
		t.Fatalf("history since a1 = %d", len(after))
	}
	var argv []string
	data, _ := os.ReadFile(filepath.Join(config, "argv.json"))
	_ = json.Unmarshal(data, &argv)
	if !slices.Contains(argv, "--session-id") || !slices.Contains(argv, "--model") {
		t.Fatalf("first start argv = %v", argv)
	}
	if err := d.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	again := New(pidriver.Options{Log: os.Stderr})
	if err := again.Start(spec); err != nil {
		t.Fatal(err)
	}
	defer again.Stop(context.Background())
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		data, _ = os.ReadFile(filepath.Join(config, "argv.json"))
		_ = json.Unmarshal(data, &argv)
		if slices.Contains(argv, "--resume") {
			break
		}
	}
	if !slices.Contains(argv, "--resume") || slices.Contains(argv, "--session-id") {
		t.Fatalf("restart argv = %v", argv)
	}
}

func TestInterruptOutsideTurnIsPlainMessage(t *testing.T) {
	d, _ := start(t, t.TempDir())
	if err := d.Send(pidriver.Outgoing{Text: "stop and do this", Kind: pidriver.Interrupt}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, "agent_settled")
}

func TestInterruptDuringTurnWaitsForConfirmation(t *testing.T) {
	d, _ := start(t, t.TempDir())
	d.mu.Lock()
	d.state.Streaming = true
	d.mu.Unlock()
	if err := d.Send(pidriver.Outgoing{Text: "now this", Kind: pidriver.Interrupt}); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, d, "message_start")
	if ev.Data["message"].(map[string]any)["role"] != "user" {
		t.Fatalf("first event = %+v", ev)
	}
	d.mu.Lock()
	pending := d.interrupting
	d.mu.Unlock()
	if pending != "" {
		t.Fatal("interrupt not confirmed")
	}
	waitFor(t, d, "agent_settled")
}

func TestAPIKeyRefused(t *testing.T) {
	d, _ := start(t, t.TempDir(), "FAKE_API_KEY_SOURCE=ANTHROPIC_API_KEY")
	if err := d.Send(pidriver.Outgoing{Text: "hello", Kind: pidriver.Regular}); err != nil {
		t.Fatal(err)
	}
	select {
	case exit := <-d.Exited():
		if !errors.Is(exit.Err, ErrAPIKey) {
			t.Fatalf("exit = %+v", exit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claude not stopped")
	}
}
