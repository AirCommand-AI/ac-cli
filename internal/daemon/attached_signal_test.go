package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	sup "github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type signalPoll struct{}

func (signalPoll) Fetch(context.Context, sup.AgentDefinition, string, bool) (sup.Feed, error) {
	return sup.Feed{Cursor: "baseline"}, nil
}
func (signalPoll) Spool(_ context.Context, _ sup.AgentDefinition, n sup.Notification) (any, error) {
	return map[string]any{"messageId": n.MessageID, "summary": "message pointer"}, nil
}
func (signalPoll) MessageBody(context.Context, sup.AgentDefinition, string) (string, error) {
	return "review live work", nil
}
func TestAttachedControlStreamDeliversInterruptAndNudge(t *testing.T) {
	home := t.TempDir()
	m := sup.New(home, "pi", "aircom", nil, signalPoll{})
	if err := credentials.NewStore(home).Save(credentials.Credential{AgentID: "agm_signal", WorkstreamCode: "478", APIToken: "token", SocketAddress: "ac:agm_signal"}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(shortSocketDir(t), "sig.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go serveConnection(ctx, conn, m, time.Now(), "test", "", os.Getpid(), cancel, &atomic.Bool{}, nil)
		}
	}()
	open := func() (net.Conn, *bufio.Reader) {
		c, e := net.Dial("unix", listener.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		return c, bufio.NewReader(c)
	}
	send := func(c net.Conn, v any) {
		t.Helper()
		if err := json.NewEncoder(c).Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	read := func(r *bufio.Reader) map[string]any {
		t.Helper()
		line, e := r.ReadBytes('\n')
		if e != nil {
			t.Fatal(e)
		}
		var result map[string]any
		if e = json.Unmarshal(line, &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	claim, cr := open()
	send(claim, Request{Op: "agent.claim", AgentID: "agm_signal", Workstream: "478"})
	if got := read(cr)["ok"]; got != true {
		t.Fatal(got)
	}
	send(claim, Request{Op: "session.attach", AgentID: "agm_signal", Name: "signal", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid())})
	if got := read(cr)["ok"]; got != true {
		t.Fatal(got)
	}
	claim.Close()
	stream, sr := open()
	send(stream, Request{Op: "session.subscribe", AgentID: "agm_signal", SessionPID: os.Getpid()})
	_ = read(sr)
	if got := read(sr)["type"]; got != "connect" {
		t.Fatalf("connect %v", got)
	}
	defer stream.Close()
	for _, kind := range []string{"interrupt", "nudge"} {
		n := sup.Notification{Type: "message.received", Kind: kind, MessageID: "msg-" + kind, SenderID: "agm_sender"}
		if err := m.Wake(ctx, "agm_signal", n); err != nil {
			t.Fatal(err)
		}
		var signal map[string]any
		for i := 0; i < 2; i++ {
			event := read(sr)
			if event["type"] == kind {
				signal = event
			}
		}
		if signal == nil || !strings.Contains(signal["text"].(string), "msg-"+kind) {
			t.Fatalf("%s signal missing: %v", kind, signal)
		}
		if kind == "interrupt" && !strings.Contains(signal["text"].(string), "review live work") {
			t.Fatal("interrupt body omitted")
		}
	}
}
