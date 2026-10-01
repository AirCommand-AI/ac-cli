//go:build pi_integration

package pidriver

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"
)

var piPath = flag.String("pi-path", "pi", "pi executable for integration tests")

func TestRealPiRPC(t *testing.T) {
	d := New(Options{})
	if err := d.Start(LaunchSpec{PiPath: *piPath, WorkDir: t.TempDir(), SessionID: "pidriver-integration-" + time.Now().Format("20060102150405")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := d.Stop(stopCtx); err != nil {
			t.Error(err)
		}
		select {
		case <-d.Exited():
		case <-time.After(time.Second):
			t.Error("Exited not reported after Stop")
		}
	}()
	select {
	case <-d.Ready():
	case <-ctx.Done():
		t.Fatal("get_state did not succeed")
	}
	if err := d.Send(Outgoing{Text: "Say hello", Kind: Regular}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(90 * time.Second)
	sawResponse, sawUser := false, false
	var userRecord map[string]any
	for {
		select {
		case ev := <-d.Events():
			if ev.Kind == "response" && ev.Data["command"] == "prompt" && ev.Data["success"] == true {
				sawResponse = true
			}
			if ev.Kind == "message_start" {
				if msg, ok := ev.Data["message"].(map[string]any); ok && msg["role"] == "user" {
					userRecord = msg
					if blocks, ok := msg["content"].([]any); ok {
						for _, block := range blocks {
							if part, ok := block.(map[string]any); ok {
								if text, ok := part["text"].(string); ok && strings.HasPrefix(text, "[AirCommand] ") {
									sawUser = true
								}
							}
						}
					}
				}
			}
			if ev.Kind == "agent_settled" {
				if !sawResponse || !sawUser {
					t.Fatalf("missing prompt acceptance or prefixed user message: response=%v user=%v record=%v", sawResponse, sawUser, userRecord)
				}
				return
			}
		case <-deadline:
			t.Fatal("pi did not settle")
		}
	}
}
