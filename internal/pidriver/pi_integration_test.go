//go:build pi_integration

package pidriver

import (
	"context"
	"flag"
	"testing"
	"time"
)

var piPath = flag.String("pi-path", "pi", "pi executable for integration tests")

func TestRealPiRPC(t *testing.T) {
	d := New()
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
	for {
		select {
		case ev := <-d.Events():
			if ev.Kind == "agent_settled" {
				return
			}
		case <-deadline:
			t.Fatal("pi did not settle")
		}
	}
}
