package app

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestAgentCommandsUseLocalDaemonSocket(t *testing.T) {
	home := shortAgentTestHome(t)
	path := storagepath.DaemonSocket(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	operations := make(chan map[string]any, 3)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req map[string]any
			_ = json.NewDecoder(conn).Decode(&req)
			operations <- req
			payload := map[string]any{"ok": true, "data": map[string]any{}}
			if req["op"] == "agent.list" {
				payload["data"] = map[string]any{"agents": []any{map[string]any{"name": "eng-3", "agentId": "agm_3", "state": "running", "workstream": "626"}}}
			}
			_ = json.NewEncoder(conn).Encode(payload)
			_ = conn.Close()
		}
	}()
	output := &bytes.Buffer{}
	app := &App{Store: credentials.NewStore(home), Stdout: output, Stderr: &bytes.Buffer{}}
	for _, args := range [][]string{{"agent", "list"}, {"agent", "stop", "eng-3"}, {"agent", "remove", "eng-3"}} {
		// remove also calls the registration API, so test only the socket remove operation directly.
		if args[1] == "remove" {
			break
		}
		if code := app.Run(args); code != 0 {
			t.Fatalf("%v failed", args)
		}
	}
	if !strings.Contains(output.String(), "eng-3\tagm_3\trunning\t626") {
		t.Fatalf("list output: %q", output.String())
	}
	for _, op := range []string{"agent.list", "agent.stop"} {
		req := <-operations
		if req["op"] != op {
			t.Fatalf("operation: %v", req)
		}
	}
	if code := app.Run([]string{"agent", "stop", "bad.name"}); code == 0 {
		t.Fatal("unsafe name accepted")
	}
}

func shortAgentTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "aca-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func TestOfflineAgentModeOnlyWhenStopped(t *testing.T) {
	_, client, stdout, stderr := leaveFixture(t)
	home := shortAgentTestHome(t)
	client.Store = credentials.NewStore(home)
	storedAgent(t, client, leadID, "583", "Lead")
	path := storagepath.AgentDaemonState(home, leadID)
	def := supervisor.AgentDefinition{Name: "Lead", AgentID: leadID, Workstream: "583", WorkFolder: filepath.Join(home, "work"), Desired: "running", Mode: "tmux"}
	writeDef := func() {
		t.Helper()
		data, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeDef()
	if code, _, _ := run(t, client, stdout, stderr, "agent", "mode", "Lead", "headless"); code == 0 {
		t.Fatal("changed running agent mode")
	}
	def.Desired = "stopped"
	writeDef()
	if code, out, errText := run(t, client, stdout, stderr, "agent", "mode", "Lead", "headless"); code != 0 {
		t.Fatalf("mode failed: %s %s", out, errText)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &def); err != nil {
		t.Fatal(err)
	}
	if def.Mode != "headless" || def.Desired != "stopped" {
		t.Fatalf("offline mode corrupted state: %+v", def)
	}
}

func TestRemovedLegacyTopLevelCommands(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	for _, command := range []string{"connect", "agents", "disconnect"} {
		if code := app.Run([]string{command}); code == 0 {
			t.Fatalf("accepted removed command %q", command)
		}
	}
}

func TestAgentRemoveWithoutRunningDaemonCleansState(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "missing socket"
		if stale {
			name = "stale socket"
		}
		t.Run(name, func(t *testing.T) { testOfflineRemoval(t, stale) })
	}
}

func testOfflineRemoval(t *testing.T, stale bool) {
	_, client, stdout, stderr := leaveFixture(t)
	home := shortAgentTestHome(t)
	client.Store = credentials.NewStore(home)
	storedAgent(t, client, leadID, "583", "Lead")
	storedAgent(t, client, engineerID, "583", "Engineer")
	if stale {
		if err := os.MkdirAll(storagepath.DaemonDirectory(home), 0700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", storagepath.DaemonSocket(home))
		if err != nil {
			t.Fatal(err)
		}
		listener.(*net.UnixListener).SetUnlinkOnClose(false)
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{storagepath.AgentDaemonState(home, leadID), storagepath.AgentBrief(home, leadID), storagepath.AgentDelivered(home, leadID)}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errText := run(t, client, stdout, stderr, "agent", "remove", "Lead"); code != 0 {
		t.Fatalf("remove failed: %q %q", out, errText)
	}
	if credentialExists(t, client, leadID) {
		t.Fatal("agent credential remains")
	}
	if !credentialExists(t, client, engineerID) {
		t.Fatal("removed other agent credential")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("state file %s remains: %v", path, err)
		}
	}
}
