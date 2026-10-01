package daemon

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestShutdownClosesStreamingControlConnections(t *testing.T) {
	for _, op := range []string{"agent.attach", "agent.takeover"} {
		t.Run(op, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			f := &attachSupervisor{fakeSupervisor: &fakeSupervisor{}, driver: pidriver.NewFake(), events: make(chan pidriver.Event)}
			done := make(chan struct{})
			go func() {
				defer close(done)
				serveConnection(ctx, server, f, time.Now(), "dev", "", 0, func() {}, new(atomic.Bool), nil)
			}()
			_ = client.SetDeadline(time.Now().Add(time.Second))
			enc := json.NewEncoder(client)
			dec := json.NewDecoder(client)
			_ = enc.Encode(Request{Op: op, Name: "eng-1"})
			var response Response
			if err := dec.Decode(&response); err != nil || !response.OK {
				t.Fatalf("ready %+v %v", response, err)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown blocked on stream")
			}
		})
	}
}
