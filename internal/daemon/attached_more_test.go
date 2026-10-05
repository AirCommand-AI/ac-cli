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

	sup "github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestClaimConnectionCloseReleasesLock(t *testing.T) {
	home := t.TempDir()
	m := sup.New(home, "pi", "aircom", nil, nil)
	l, err := net.Listen("unix", filepath.Join(shortSocketDir(t), "c.sock"))
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
	claim := func() (net.Conn, error) {
		c, e := net.Dial("unix", l.Addr().String())
		if e != nil {
			return nil, e
		}
		_ = c.SetDeadline(time.Now().Add(time.Second))
		e = json.NewEncoder(c).Encode(Request{Op: "agent.claim", AgentID: "agm_free"})
		if e != nil {
			c.Close()
			return nil, e
		}
		var response Response
		e = json.NewDecoder(bufio.NewReader(c)).Decode(&response)
		if e != nil {
			c.Close()
			return nil, e
		}
		if !response.OK {
			c.Close()
			return nil, ErrAlreadyRunning
		}
		return c, nil
	}
	c, err := claim()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	deadline := time.Now().Add(time.Second)
	for {
		next, e := claim()
		if e == nil {
			next.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("claim lock held after connection closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestOtherStreamCloseMarksNoListener(t *testing.T) {
	home := t.TempDir()
	m := sup.New(home, "pi", "aircom", nil, nil)
	l, err := net.Listen("unix", filepath.Join(shortSocketDir(t), "o.sock"))
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
	open := func() net.Conn {
		c, e := net.Dial("unix", l.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		return c
	}
	claim := open()
	_ = json.NewEncoder(claim).Encode(Request{Op: "agent.claim", AgentID: "agm_other"})
	var r Response
	if e := json.NewDecoder(claim).Decode(&r); e != nil || !r.OK {
		t.Fatalf("claim %+v %v", r, e)
	}
	_ = json.NewEncoder(claim).Encode(Request{Op: "session.attach", AgentID: "agm_other", Name: "other", Program: "other", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid())})
	if e := json.NewDecoder(claim).Decode(&r); e != nil || !r.OK {
		t.Fatalf("attach %+v %v", r, e)
	}
	claim.Close()
	if d, _ := m.Attached("agm_other"); d.State != "stopped" || d.Reason != "no listener" {
		t.Fatalf("other started without listener: %+v", d)
	}
	stream := open()
	_ = json.NewEncoder(stream).Encode(Request{Op: "session.subscribe", SessionPID: os.Getpid()})
	reader := bufio.NewReader(stream)
	for i := 0; i < 2; i++ {
		if _, e := reader.ReadBytes('\n'); e != nil {
			t.Fatal(e)
		}
	}
	if d, _ := m.Attached("agm_other"); d.State != "running" {
		t.Fatalf("other not running with listener: %+v", d)
	}
	stream.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d, _ := m.Attached("agm_other")
		if d.State == "stopped" && d.Reason == "no listener" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("other stream close not observed: %+v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
