package daemonclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fakeDaemon(t *testing.T, respond func(map[string]any) any) Client {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "acd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	path := filepath.Join(home, "daemon.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var req map[string]any
				if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if err := json.NewEncoder(conn).Encode(respond(req)); err != nil {
					t.Error(err)
				}
			}()
		}
	}()
	return Client{SocketPath: path}
}

func TestC3RequestsAndResponses(t *testing.T) {
	var requests []map[string]any
	client := fakeDaemon(t, func(req map[string]any) any {
		requests = append(requests, req)
		switch req["op"] {
		case "status":
			return map[string]any{"ok": true, "data": map[string]any{"version": 1, "pid": 42, "agents": []any{}}}
		case "agent.list":
			return map[string]any{"ok": true, "data": map[string]any{"agents": []any{map[string]any{"name": "eng-3", "agentId": "agm_3"}}}}
		default:
			return map[string]any{"ok": true, "data": map[string]any{}}
		}
	})
	ctx := context.Background()
	status, err := client.Status(ctx)
	if err != nil || status.PID != 42 {
		t.Fatalf("status: %+v, %v", status, err)
	}
	agents, err := client.List(ctx)
	if err != nil || len(agents) != 1 || agents[0].AgentID != "agm_3" {
		t.Fatalf("agents: %+v, %v", agents, err)
	}
	start := StartRequest{Name: "eng-3", AgentID: "agm_3", Organization: "Air Command", Workstream: "626", Repos: []string{"AirCommand-AI/ac-cli"}, WorkFolder: "/work/eng-3"}
	if err := client.Start(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(ctx, "eng-3"); err != nil {
		t.Fatal(err)
	}
	if err := client.Remove(ctx, "eng-3"); err != nil {
		t.Fatal(err)
	}
	if err := client.Shutdown(ctx, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"status", "agent.list", "agent.start", "agent.stop", "agent.remove", "shutdown"}
	for i, op := range want {
		if requests[i]["op"] != op {
			t.Fatalf("request %d: %v", i, requests[i])
		}
	}
	if requests[2]["workFolder"] != start.WorkFolder || !reflect.DeepEqual(requests[2]["repos"], []any{"AirCommand-AI/ac-cli"}) {
		t.Fatalf("start request: %v", requests[2])
	}
	if requests[5]["stopAgents"] != true {
		t.Fatalf("shutdown: %v", requests[5])
	}
}

func TestDaemonErrorAndUnavailableSocket(t *testing.T) {
	client := fakeDaemon(t, func(map[string]any) any {
		return map[string]any{"ok": false, "error": map[string]any{"code": "locked", "message": "agent in use"}}
	})
	err := client.Stop(context.Background(), "eng-3")
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "locked" {
		t.Fatalf("expected locked error, got %v", err)
	}
	client.SocketPath = filepath.Join(t.TempDir(), "absent.sock")
	if _, err := os.Stat(client.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("unexpected socket: %v", err)
	}
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("missing socket accepted")
	}
}
