package pidriver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test subprocess acts as pi; it deliberately does not require a local pi installation.
func TestFakePi(t *testing.T) {
	if os.Getenv("PIDRIVER_FAKE_PI") != "1" {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("pi 0.99.1")
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var m map[string]any
		if dec.Decode(&m) != nil {
			return
		}
		typ := m["type"]
		id := m["id"]
		switch typ {
		case "get_state":
			_ = enc.Encode(map[string]any{"type": "response", "id": id, "command": "get_state", "success": true, "data": map[string]any{}})
		case "prompt":
			_ = enc.Encode(map[string]any{"type": "response", "id": id, "command": "prompt", "success": true, "data": map[string]any{"disposition": "started"}})
			_ = enc.Encode(map[string]any{"type": "agent_start"})
			_ = enc.Encode(map[string]any{"type": "agent_settled"})
		default:
			_ = enc.Encode(map[string]any{"type": "response", "id": id, "command": typ, "success": true})
		}
	}
}
func TestRPCFakeProcess(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_PI", "1")
	path := filepath.Join(t.TempDir(), "pi")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pi 0.99.1'; else exec %q -test.run=^TestFakePi$; fi\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	d := New()
	if err := d.Send(Outgoing{Text: "/not-a-command", Kind: Urgent}); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("never ready")
	}
	deadline := time.After(5 * time.Second)
	seen := false
	for !seen {
		select {
		case ev := <-d.Events():
			if ev.Kind == "agent_settled" {
				seen = true
			}
		case <-deadline:
			t.Fatal("no events")
		}
	}
	if !d.State().Settled {
		t.Fatal("expected settled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestVersion(t *testing.T) {
	for _, v := range []string{"0.87.0", "0.86.99"} {
		m := versionRE.FindStringSubmatch(v)
		if m == nil || !strings.Contains(v, ".") {
			t.Fatal(v)
		}
	}
}
