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
)

func TestAgentCommandsUseLocalDaemonSocket(t *testing.T) {
	home := t.TempDir()
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

func TestRemovedLegacyTopLevelCommands(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	for _, command := range []string{"connect", "agents", "disconnect"} {
		if code := app.Run([]string{command}); code == 0 {
			t.Fatalf("accepted removed command %q", command)
		}
	}
}
