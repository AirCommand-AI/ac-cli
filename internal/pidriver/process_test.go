package pidriver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBinary(t *testing.T) string {
	t.Helper()
	t.Setenv("PIDRIVER_FAKE_PI", "1")
	path := filepath.Join(t.TempDir(), "pi")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pi 0.99.1'; else exec %q -test.run=^TestFakePi$; fi\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func runFake(t *testing.T, path string) Driver {
	t.Helper()
	d := New(Options{})
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	return d
}
func waitEvent(t *testing.T, d Driver, kind string) Event {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-d.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-timer.C:
			t.Fatalf("missing %s", kind)
		}
	}
}
func TestProcessQueueBeforeReadyAndNoDisposition(t *testing.T) {
	t.Setenv("PIDRIVER_TRACE_COMMANDS", "1")
	t.Setenv("PIDRIVER_FAKE_NO_DISPOSITION", "1")
	path := fakeBinary(t)
	d := New(Options{})
	for _, text := range []string{"one", "two", "three"} {
		if err := d.Send(Outgoing{Text: text, Kind: Regular}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Start(LaunchSpec{PiPath: path, WorkDir: ".", SessionID: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	for _, want := range []string{"one", "two", "three"} {
		ev := waitEvent(t, d, "fake_command")
		if ev.Data["command"] == "get_state" {
			ev = waitEvent(t, d, "fake_command")
		}
		if ev.Data["command"] != "prompt" || !strings.Contains(fmt.Sprint(ev.Data["message"]), want) {
			t.Fatalf("prompt order: want %s got %+v", want, ev.Data)
		}
	}
}
func TestProcessEOFWhileToolRunning(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_ABORT_HANG", "1")
	d := runFake(t, fakeBinary(t))
	<-d.Ready()
	if err := d.Send(Outgoing{Text: "working"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, d, "tool_execution_start")
	rpc := d.(*RPC)
	if err := rpc.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Exited():
	case <-time.After(3 * time.Second):
		t.Fatal("pi did not exit on EOF mid-run")
	}
}
func TestProcessInterruptIdle(t *testing.T) {
	t.Setenv("PIDRIVER_TRACE_COMMANDS", "1")
	d := runFake(t, fakeBinary(t))
	<-d.Ready()
	if err := d.Send(Outgoing{Kind: Interrupt, Text: "replace"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"get_state", "clear_queue", "abort", "prompt"} {
		ev := waitEvent(t, d, "fake_command")
		if ev.Data["command"] != want {
			t.Fatalf("want %s got %v", want, ev.Data)
		}
	}
}
func TestProcessSlowEventsReader(t *testing.T) {
	t.Setenv("PIDRIVER_FAKE_BURST", "1")
	d := runFake(t, fakeBinary(t))
	<-d.Ready()
	if err := d.Send(Outgoing{Text: "burst"}); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not consume Events while pi writes more than the channel cap.
	time.Sleep(200 * time.Millisecond)
	if err := d.Send(Outgoing{Text: "next"}); err != nil {
		t.Fatal(err)
	}
	_ = d.State()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestProcessInterruptDuringToolAndIdle(t *testing.T) {
	t.Setenv("PIDRIVER_TRACE_COMMANDS", "1")
	t.Setenv("PIDRIVER_FAKE_ABORT_HANG", "1")
	d := runFake(t, fakeBinary(t))
	<-d.Ready()
	if err := d.Send(Outgoing{Text: "work"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, d, "tool_execution_start")
	if err := d.Send(Outgoing{Text: "stop", Kind: Interrupt}); err != nil {
		t.Fatal(err)
	}
	// Commands must be clear_queue, abort, then the replacement prompt.
	for _, want := range []string{"clear_queue", "abort", "prompt"} {
		ev := waitEvent(t, d, "fake_command")
		if ev.Data["command"] != want {
			t.Fatalf("want %s got %v", want, ev.Data)
		}
	}
}
