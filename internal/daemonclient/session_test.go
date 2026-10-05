package daemonclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestSubscribeSessionStreamsAfterEnvelope(t *testing.T) {
	client := Client{SocketPath: "fake", Dial: func(context.Context, string, string) (net.Conn, error) {
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			var req map[string]any
			dec := json.NewDecoder(server)
			if err := dec.Decode(&req); err != nil {
				t.Error(err)
				return
			}
			if req["op"] != "session.subscribe" || req["sessionPid"] != float64(42) {
				t.Errorf("request: %+v", req)
			}
			_, _ = io.WriteString(server, "{\"ok\":true}\n{\"type\":\"connect\",\"agentId\":\"agm_test\",\"offset\":23}\n{\"type\":\"wake\",\"line\":\"Task 9 assigned\"}\n{\"type\":\"detached\",\"reason\":\"pi closed\"}\n")
		}()
		return peer, nil
	}}
	var messages []SessionMessage
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.SubscribeSession(ctx, 42, func(m SessionMessage) error { messages = append(messages, m); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Offset != 23 || messages[1].Line != "Task 9 assigned" || messages[2].Reason != "pi closed" {
		t.Fatalf("messages: %+v", messages)
	}
}
func TestSessionOpsUseDaemonProtocol(t *testing.T) {
	requests := make(chan map[string]any, 4)
	client := Client{SocketPath: "fake", Dial: func(context.Context, string, string) (net.Conn, error) {
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			var req map[string]any
			if err := json.NewDecoder(server).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			requests <- req
			_, _ = io.WriteString(server, "{\"ok\":true,\"data\":{\"agentId\":\"agm_test\",\"workstream\":\"478\"}}\n")
		}()
		return peer, nil
	}}
	ctx := context.Background()
	if err := client.AttachSession(ctx, SessionAttach{AgentID: "agm_test", Workstream: "478", SessionPID: 42, SessionStart: "today", Program: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := client.SessionEvent(ctx, 42, "state", "working"); err != nil {
		t.Fatal(err)
	}
	result, err := client.LookupSession(ctx, "conv")
	if err != nil || result.AgentID != "agm_test" {
		t.Fatalf("lookup: %+v %v", result, err)
	}
	for _, want := range []string{"session.attach", "session.event", "session.lookup"} {
		if got := <-requests; got["op"] != want {
			t.Fatalf("%s: %+v", want, got)
		}
	}
}
