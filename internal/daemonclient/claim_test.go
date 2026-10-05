package daemonclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
)

func TestClaimAndAttachUseTheSameConnection(t *testing.T) {
	done := make(chan error, 1)
	client := Client{SocketPath: "fake", Dial: func(context.Context, string, string) (net.Conn, error) {
		server, peer := net.Pipe()
		go func() {
			defer server.Close()
			dec := json.NewDecoder(server)
			for _, op := range []string{"agent.claim", "session.attach"} {
				var req map[string]any
				if err := dec.Decode(&req); err != nil {
					done <- err
					return
				}
				if req["op"] != op {
					t.Errorf("got %+v, want %s", req, op)
				}
				if op == "session.attach" && req["sessionStart"] != "123" {
					t.Errorf("attach=%+v", req)
				}
				_, _ = io.WriteString(server, "{\"ok\":true,\"data\":{\"agentId\":\"agm_test\"}}\n")
			}
			done <- nil
		}()
		return peer, nil
	}}
	claim, err := client.ClaimSession(context.Background(), "agm_test", "478")
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	if err := claim.AttachSession(SessionAttach{AgentID: "agm_test", Name: "Pi", Workstream: "478", SessionPID: 42, SessionStart: "123", Program: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
