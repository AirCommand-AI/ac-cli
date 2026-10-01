package daemon

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestTakeoverControlHandshake(t *testing.T) {
	server, client := net.Pipe()
	f := &fakeSupervisor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConnection(ctx, server, f, time.Now(), "dev", "", 0, func() {}, new(atomic.Bool), nil)
	}()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)
	if err := enc.Encode(Request{Op: "agent.takeover", Name: "eng-1"}); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		OK   bool `json:"ok"`
		Data struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}
	if err := dec.Decode(&reply); err != nil || !reply.OK || reply.Data.SessionID != "agm_1" {
		t.Fatalf("reply: %+v %v", reply, err)
	}
	if err := enc.Encode(map[string]any{"type": "pid", "pid": 99999999}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("takeover did not resume after socket close and pid exit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 2 || f.calls[0] != "takeover:eng-1" || f.calls[1] != "resume:eng-1" {
		t.Fatal(f.calls)
	}
}
