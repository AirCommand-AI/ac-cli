package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type failingRecordSupervisor struct {
	*fakeSupervisor
	resumeCalls atomic.Int32
}

func (f *failingRecordSupervisor) RecordTakeover(string, int) error {
	return errors.New("cannot record pid")
}
func (f *failingRecordSupervisor) ResumeTakeover(string) error { f.resumeCalls.Add(1); return nil }
func TestRecordFailureKeepsTakeoverUntilUnfencedPIDExits(t *testing.T) {
	server, client := net.Pipe()
	f := &failingRecordSupervisor{fakeSupervisor: &fakeSupervisor{}}
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
	_ = enc.Encode(Request{Op: "agent.takeover", Name: "eng-1"})
	var ready Response
	if err := dec.Decode(&ready); err != nil || !ready.OK {
		t.Fatalf("ready %+v %v", ready, err)
	}
	_ = enc.Encode(map[string]any{"type": "pid", "pid": os.Getpid()})
	var frame struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := dec.Decode(&frame); err != nil || frame.Type != "error" {
		t.Fatalf("missing error frame %+v %v", frame, err)
	}
	_ = client.Close()
	select {
	case <-done:
		t.Fatal("resumed while unfenced foreground pid alive")
	case <-time.After(200 * time.Millisecond):
	}
	if got := f.resumeCalls.Load(); got != 0 {
		t.Fatalf("resumed %d times", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}

type waitingTakeoverSupervisor struct {
	*fakeSupervisor
	released atomic.Bool
}

func (f *waitingTakeoverSupervisor) ResumeTakeover(name string) error {
	if !f.released.Load() {
		return errors.New("foreground pi still running")
	}
	f.record("resume:" + name)
	return nil
}
func TestTakeoverWaitsForForegroundExitAfterSocketClose(t *testing.T) {
	server, client := net.Pipe()
	f := &waitingTakeoverSupervisor{fakeSupervisor: &fakeSupervisor{}}
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
	_ = enc.Encode(Request{Op: "agent.takeover", Name: "eng-1"})
	var ready Response
	if err := dec.Decode(&ready); err != nil || !ready.OK {
		t.Fatalf("ready %+v %v", ready, err)
	}
	_ = enc.Encode(map[string]any{"type": "pid", "pid": 12345})
	_ = client.Close()
	select {
	case <-done:
		t.Fatal("resumed while foreground pi alive")
	case <-time.After(150 * time.Millisecond):
	}
	f.released.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not resume after pi exit")
	}
}
func TestTakeoverWithoutPIDResumesOnDisconnect(t *testing.T) {
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
	_ = json.NewEncoder(client).Encode(Request{Op: "agent.takeover", Name: "eng-1"})
	var reply Response
	if err := json.NewDecoder(client).Decode(&reply); err != nil || !reply.OK {
		t.Fatalf("reply %+v %v", reply, err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("no-pid takeover did not resume")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 2 || f.calls[0] != "takeover:eng-1" || f.calls[1] != "resume:eng-1" {
		t.Fatal(f.calls)
	}
}
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
	if len(f.calls) != 3 || f.calls[0] != "takeover:eng-1" || f.calls[1] != "pid:eng-1" || f.calls[2] != "resume:eng-1" {
		t.Fatal(f.calls)
	}
}
