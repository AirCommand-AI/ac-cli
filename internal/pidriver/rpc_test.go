package pidriver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
	if os.Getenv("PIDRIVER_HANG_ON_EOF") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var m map[string]any
		if dec.Decode(&m) != nil {
			if os.Getenv("PIDRIVER_HANG_ON_EOF") == "1" {
				for {
					time.Sleep(time.Hour)
				}
			}
			return
		}
		typ := m["type"]
		id := m["id"]
		switch typ {
		case "get_state":
			_ = enc.Encode(map[string]any{"type": "response", "id": id, "command": "get_state", "success": true, "data": map[string]any{}})
			if os.Getenv("PIDRIVER_STALL_READER") == "1" {
				signal.Ignore(syscall.SIGTERM)
				for {
					time.Sleep(time.Hour)
				}
			}
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
	d := New(Options{})
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
func TestStopWithoutDeadline(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_PI", "1")
	t.Setenv("PIDRIVER_HANG_ON_EOF", "1")
	path := filepath.Join(t.TempDir(), "pi")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pi 0.99.1'; else exec %q -test.run=^TestFakePi$; fi\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	d := New(Options{})
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("not ready")
	}
	begin := time.Now()
	if err := d.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed < 5*time.Second || elapsed > 10*time.Second {
		t.Fatalf("stop took %v, expected grace then group termination", elapsed)
	}
	select {
	case <-d.Exited():
	default:
		t.Fatal("Stop consumed Exited")
	}
}
func TestStalledStdinReaderDoesNotLockDriver(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_PI", "1")
	t.Setenv("PIDRIVER_STALL_READER", "1")
	path := filepath.Join(t.TempDir(), "pi")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pi 0.99.1'; else exec %q -test.run=^TestFakePi$; fi\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	d := New(Options{})
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Ready():
	case <-time.After(time.Second):
		t.Fatal("not ready")
	}
	if err := d.Send(Outgoing{Kind: Regular, Text: strings.Repeat("x", 1024*1024)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	result := make(chan error, 1)
	go func() { _ = d.State(); result <- d.Send(Outgoing{Text: "small"}) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer blocked State/Send")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestStopAlreadyCancelledContext(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_PI", "1")
	t.Setenv("PIDRIVER_HANG_ON_EOF", "1")
	path := filepath.Join(t.TempDir(), "pi")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pi 0.99.1'; else exec %q -test.run=^TestFakePi$; fi\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	d := New(Options{})
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("not ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	begin := time.Now()
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed < 3*time.Second || elapsed > 5*time.Second {
		t.Fatalf("cancelled-context Stop skipped SIGTERM grace: %v", elapsed)
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
