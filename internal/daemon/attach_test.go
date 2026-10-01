package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

type attachSupervisor struct {
	*fakeSupervisor
	driver  *pidriver.Fake
	events  chan pidriver.Event
	history []json.RawMessage
}

func (f *attachSupervisor) Driver(string) (pidriver.Driver, bool) { return f.driver, true }
func (f *attachSupervisor) History(_ string, _ string, limit int) ([]json.RawMessage, string, error) {
	if len(f.history) > limit {
		return f.history[len(f.history)-limit:], "", nil
	}
	return f.history, "", nil
}
func (f *attachSupervisor) Subscribe(string) (<-chan pidriver.Event, func(), bool) {
	return f.events, func() {}, true
}

func TestAttachSlowViewerGetsDetachBanner(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	events := make(chan pidriver.Event)
	close(events)
	f := &attachSupervisor{fakeSupervisor: &fakeSupervisor{}, driver: pidriver.NewFake(), events: events}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveConnection(ctx, server, f, time.Now(), "dev", "", 0, func() {}, new(atomic.Bool), nil)
	_ = client.SetDeadline(time.Now().Add(time.Second))
	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)
	_ = enc.Encode(Request{Op: "agent.attach", Name: "eng-1"})
	var ready Response
	if err := dec.Decode(&ready); err != nil || !ready.OK {
		t.Fatalf("ready %v %+v", err, ready)
	}
	var banner struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := dec.Decode(&banner); err != nil || banner.Type != "banner" || banner.Text != "viewer too slow, detached" {
		t.Fatalf("banner %v %+v", err, banner)
	}
}
func TestAttachStreamAndDetach(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	f := &attachSupervisor{fakeSupervisor: &fakeSupervisor{}, driver: pidriver.NewFake(), events: make(chan pidriver.Event, 2), history: []json.RawMessage{json.RawMessage(`{"id":"1","message":{"role":"user","content":"first"}}`), json.RawMessage(`{"id":"2","message":{"role":"assistant","content":"second"}}`)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConnection(ctx, server, f, time.Now(), "dev", "", 0, func() {}, new(atomic.Bool), nil)
	}()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	enc := json.NewEncoder(client)
	dec := json.NewDecoder(bufio.NewReader(client))
	if err := enc.Encode(Request{Op: "agent.attach", Name: "eng-1"}); err != nil {
		t.Fatal(err)
	}
	var ready Response
	if err := dec.Decode(&ready); err != nil || !ready.OK {
		t.Fatalf("ready: %v %+v", err, ready)
	}
	for _, want := range []string{"1", "2"} {
		var record struct {
			Type  string `json:"type"`
			Entry struct {
				ID string `json:"id"`
			} `json:"entry"`
		}
		if err := dec.Decode(&record); err != nil || record.Type != "history" || record.Entry.ID != want {
			t.Fatalf("history order: %+v %v", record, err)
		}
	}
	f.events <- pidriver.Event{Kind: "tool_execution_start", Data: map[string]any{"toolName": "bash"}}
	var event struct {
		Type  string         `json:"type"`
		Event pidriver.Event `json:"event"`
	}
	if err := dec.Decode(&event); err != nil || event.Type != "event" || event.Event.Kind != "tool_execution_start" {
		t.Fatalf("event: %v %+v", err, event)
	}
	if err := enc.Encode(Request{Op: "say", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	// Detach leaves pi running; only this viewer socket is closed.
	if err := enc.Encode(Request{Op: "detach"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if len(f.driver.Sent) != 1 || f.driver.Sent[0].Kind != pidriver.Urgent || f.driver.Sent[0].Text != "hello" {
		t.Fatalf("sent: %+v", f.driver.Sent)
	}
}
