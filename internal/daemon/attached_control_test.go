package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	sup "github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "acd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func TestSessionClaimAttachSubscribeAndAck(t *testing.T) {
	home := t.TempDir()
	m := sup.New(home, "pi", "aircom", nil, nil)
	if err := credentials.NewStore(home).Save(credentials.Credential{AgentID: "agm_person", WorkstreamCode: "478", APIToken: "token", SocketKey: "key", SocketAddress: "ac:agm_person"}); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(shortSocketDir(t), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go serveConnection(ctx, c, m, time.Now(), "test", "", os.Getpid(), cancel, &atomic.Bool{}, nil)
		}
	}()
	open := func() (net.Conn, *bufio.Reader) {
		c, e := net.Dial("unix", l.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		return c, bufio.NewReader(c)
	}
	send := func(c net.Conn, v any) {
		t.Helper()
		if e := json.NewEncoder(c).Encode(v); e != nil {
			t.Fatal(e)
		}
	}
	read := func(r *bufio.Reader) map[string]any {
		t.Helper()
		var v map[string]any
		line, e := r.ReadBytes('\n')
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(line, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	pending, pr := open()
	send(pending, Request{Op: "session.subscribe", SessionPID: os.Getpid()})
	if got := read(pr)["ok"]; got != true {
		t.Fatalf("pending subscribe rejected: %v", got)
	}
	claim, cr := open()
	send(claim, Request{Op: "agent.claim", AgentID: "agm_person", Workstream: "478"})
	if !read(cr)["ok"].(bool) {
		t.Fatal("claim refused")
	}
	send(claim, Request{Op: "session.attach", AgentID: "agm_person", Name: "person", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid()), SessionID: "sess"})
	if !read(cr)["ok"].(bool) {
		t.Fatal("attach refused")
	}
	claim.Close()
	if got := read(pr)["type"]; got != "connect" {
		t.Fatalf("pending subscribe did not connect: %v", got)
	}
	pending.Close()
	lookup, lr := open()
	send(lookup, Request{Op: "session.lookup", SessionID: "sess"})
	if !read(lr)["ok"].(bool) {
		t.Fatal("lookup failed")
	}
	lookup.Close()
	stream, sr := open()
	send(stream, Request{Op: "session.subscribe", AgentID: "agm_person", SessionID: "sess", SessionPID: os.Getpid()})
	if got := read(sr)["ok"]; got != true {
		t.Fatalf("stream rejected: %v", got)
	}
	if got := read(sr)["type"]; got != "connect" {
		t.Fatalf("connect: %v", got)
	}
	if err := listenstore.NewStore(home).AppendNotification("agm_person", map[string]string{"messageId": "m1", "summary": "New message pointer"}); err != nil {
		t.Fatal(err)
	}
	wake := read(sr)
	if wake["type"] != "wake" || wake["line"] != "New message pointer" {
		t.Fatalf("wake: %+v", wake)
	}
	stream.Close()
	offset := int64(wake["offset"].(float64))
	ack, ar := open()
	send(ack, Request{Op: "session.ack", AgentID: "agm_person", SessionPID: os.Getpid(), Offset: offset})
	if !read(ar)["ok"].(bool) {
		t.Fatal("ack failed")
	}
	ack.Close()
	replay, rr := open()
	send(replay, Request{Op: "session.subscribe", AgentID: "agm_person", SessionPID: os.Getpid()})
	if got := read(rr)["ok"]; got != true {
		t.Fatalf("replay rejected: %v", got)
	}
	if got := read(rr)["offset"]; got != float64(offset) {
		t.Fatalf("replay offset: %v", got)
	}
	replay.Close()
	bad, br := open()
	send(bad, Request{Op: "session.attach", AgentID: "agm_person", SessionID: "sess", Program: "pi", SessionPID: 1, SessionStart: "forged"})
	if got := read(br)["error"]; got == nil {
		t.Fatal("forged pid admitted")
	}
	bad.Close()
	wrong, wr := open()
	send(wrong, Request{Op: "session.attach", AgentID: "agm_person", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: "wrong-start"})
	if result := read(wr)["error"]; result == nil || result.(map[string]any)["code"] != "held" {
		t.Fatalf("different start token admitted: %v", result)
	}
	wrong.Close()
	update, ur := open()
	send(update, Request{Op: "session.attach", AgentID: "agm_person", Name: "person", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid()), SessionID: "new-session"})
	if got := read(ur)["ok"]; got != true {
		t.Fatalf("same-session attach refused: %v", got)
	}
	update.Close()
	if d, found := m.SessionLookup("new-session"); !found || d.SessionPID != os.Getpid() {
		t.Fatal("sessionId not updated on idempotent attach")
	}
	events, cancelEvents := m.SubscribeAgentEvents()
	defer cancelEvents()
	activity, activityReader := open()
	send(activity, Request{Op: "session.event", SessionPID: os.Getpid(), Kind: "run_start", At: time.Now().UTC().Format(time.RFC3339Nano)})
	if result := read(activityReader)["ok"]; result != true {
		t.Fatalf("event rejected: %v", result)
	}
	activity.Close()
	select {
	case got := <-events:
		if got.Kind != "run_start" || got.AgentID != "agm_person" {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("event not exposed to state engine")
	}
	active, activeReader := open()
	send(active, Request{Op: "session.subscribe", SessionPID: os.Getpid(), SessionID: "new-session"})
	_ = read(activeReader)
	if got := read(activeReader)["type"]; got != "connect" {
		t.Fatalf("active stream: %v", got)
	}
	detach, dr := open()
	send(detach, Request{Op: "session.detach", SessionPID: os.Getpid(), Reason: "conversation changed"})
	if result := read(dr)["ok"]; result != true {
		t.Fatalf("detach rejected: %v", result)
	}
	detach.Close()
	if d, _ := m.Attached("agm_person"); d.State != "stopped" || d.Reason != "pi conversation changed" || d.SessionID != "new-session" || d.Offset != offset {
		t.Fatalf("detach discarded conversation/offset: %+v", d)
	}
	if got := read(activeReader)["type"]; got != "detached" {
		t.Fatalf("active stream not detached: %v", got)
	}
	active.Close()
	pending, pr = open()
	send(pending, Request{Op: "session.subscribe", SessionPID: os.Getpid(), SessionID: "later-session"})
	if got := read(pr)["ok"]; got != true {
		t.Fatalf("pending stream rejected: %v", got)
	}
	_ = pending.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if _, err := pr.ReadBytes('\n'); err == nil {
		t.Fatal("old conversation auto-connected to new session")
	}
	_ = pending.SetReadDeadline(time.Now().Add(3 * time.Second))
	fresh, fr := open()
	send(fresh, Request{Op: "session.attach", AgentID: "agm_person", Name: "", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid()), SessionID: "later-session"})
	if result := read(fr)["ok"]; result != true {
		t.Fatalf("reattach rejected: %v", result)
	}
	fresh.Close()
	if got := read(pr)["type"]; got != "connect" {
		t.Fatalf("pending stream did not connect after attach: %v", got)
	}
	pending.Close()
}
