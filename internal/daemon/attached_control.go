package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/peercred"
	sup "github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type sessionManager interface {
	Claim(string, string, int) (*sup.Claim, error)
	ReleaseClaim(*sup.Claim)
	Attach(*sup.Claim, sup.Attachment) error
	Attached(string) (sup.AgentDefinition, bool)
	SessionLookup(string) (sup.AgentDefinition, bool)
	SubscribeWakes(string) (<-chan struct{}, func(), error)
	SessionAck(string, int64) error
	SessionEvent(string, string, string, time.Time) error
	Detach(string, string) error
	SpoolPath(string) string
	AwaitAttachment(int) (<-chan struct{}, func())
	SessionConnected(string) error
}

func sessionFailure(conn net.Conn, code string, err error) {
	_ = json.NewEncoder(conn).Encode(Response{Error: &APIError{Code: code, Message: err.Error()}})
}
func serveSession(ctx context.Context, conn net.Conn, reader *bufio.Reader, s Supervisor, req Request) {
	m, ok := s.(sessionManager)
	if !ok {
		sessionFailure(conn, "invalid", fmt.Errorf("session control unavailable"))
		return
	}
	if req.Op == "session.lookup" {
		if req.SessionID == "" {
			sessionFailure(conn, "invalid", ErrInvalid)
			return
		}
		d, found := m.SessionLookup(req.SessionID)
		if !found {
			sessionFailure(conn, "not_found", os.ErrNotExist)
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{"agentId": d.AgentID, "name": d.Name, "workstream": d.Workstream, "program": d.Program, "state": d.State, "sessionPid": d.SessionPID, "sessionStart": d.SessionStart, "offset": d.Offset}})
		return
	}
	peer, err := peercred.PeerPID(conn)
	if err != nil {
		sessionFailure(conn, "invalid", err)
		return
	}
	var claim *sup.Claim
	if req.Op == "agent.claim" {
		if req.AgentID == "" && req.Name != "" {
			list, e := s.List(ctx)
			if e != nil {
				sessionFailure(conn, "internal", e)
				return
			}
			for _, a := range list {
				if a.Name == req.Name {
					req.AgentID = a.AgentID
					break
				}
			}
		}
		if req.AgentID == "" {
			sessionFailure(conn, "invalid", fmt.Errorf("agentId required (unknown name; resolve it before claiming)"))
			return
		}
		claim, err = m.Claim(req.AgentID, req.Workstream, peer)
		if err != nil {
			sessionFailure(conn, "held", err)
			return
		}
		defer m.ReleaseClaim(claim)
		if err = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{"agentId": req.AgentID, "claimExpiresInSeconds": 60}}); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Minute))
		line, e := reader.ReadBytes('\n')
		if e != nil {
			return
		}
		if len(line) > 1024*1024 || json.Unmarshal(line, &req) != nil || req.Op != "session.attach" {
			sessionFailure(conn, "invalid", fmt.Errorf("expected session.attach on claim socket"))
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
	}
	if req.Op == "session.attach" {
		if !peercred.Descendant(peer, req.SessionPID) {
			sessionFailure(conn, "invalid", fmt.Errorf("socket peer is not a descendant of sessionPid"))
			return
		}
		if claim == nil {
			old, found := m.Attached(req.AgentID)
			if !found || old.SessionPID != req.SessionPID && sup.SessionProcessAlive(old.SessionPID, old.SessionStart) || found && old.SessionPID == req.SessionPID && old.SessionStart != req.SessionStart && sup.SessionProcessAlive(old.SessionPID, old.SessionStart) {
				sessionFailure(conn, "held", fmt.Errorf("attach requires a claim or the same session pid and start token"))
				return
			}
		}
		err = m.Attach(claim, sup.Attachment{AgentID: req.AgentID, Name: req.Name, Workstream: req.Workstream, SessionPID: req.SessionPID, SessionStart: req.SessionStart, Program: req.Program, SessionID: req.SessionID})
		if err != nil {
			sessionFailure(conn, "invalid", err)
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{"agentId": req.AgentID}})
		return
	}
	if req.AgentID == "" && req.SessionPID > 0 {
		list, e := s.List(ctx)
		if e != nil {
			sessionFailure(conn, "internal", e)
			return
		}
		for _, a := range list {
			if a.Kind == "attached" && a.PID == req.SessionPID {
				req.AgentID = a.AgentID
				break
			}
		}
	}
	if req.Op == "session.subscribe" && req.AgentID == "" {
		if req.SessionPID <= 0 || !peercred.Descendant(peer, req.SessionPID) {
			sessionFailure(conn, "invalid", fmt.Errorf("socket peer is not a descendant of sessionPid"))
			return
		}
		servePendingSubscribe(ctx, conn, m, req.SessionPID)
		return
	}
	if req.AgentID == "" {
		sessionFailure(conn, "invalid", fmt.Errorf("attached session not found"))
		return
	}
	d, found := m.Attached(req.AgentID)
	if !found {
		if req.Op == "session.subscribe" && req.SessionPID > 0 && peercred.Descendant(peer, req.SessionPID) {
			servePendingSubscribe(ctx, conn, m, req.SessionPID)
			return
		}
		sessionFailure(conn, "not_found", os.ErrNotExist)
		return
	}
	if req.SessionPID != d.SessionPID || !sup.SessionProcessAlive(d.SessionPID, d.SessionStart) || !peercred.Descendant(peer, req.SessionPID) {
		sessionFailure(conn, "invalid", fmt.Errorf("socket peer is not a descendant of attached session"))
		return
	}
	if req.SessionID != "" && req.SessionID != d.SessionID {
		sessionFailure(conn, "invalid", fmt.Errorf("sessionId does not match attached session"))
		return
	}
	switch req.Op {
	case "session.subscribe":
		serveSessionWakes(ctx, conn, m, d)
	case "session.detach":
		reason := "pi closed"
		if d.Program == "other" {
			reason = "no listener"
		}
		if err = m.Detach(req.AgentID, reason); err != nil {
			sessionFailure(conn, "invalid", err)
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{}})
	case "session.event":
		switch req.Kind {
		case "run_start", "turn", "tool_start", "tool_end", "run_end":
		case "state":
			switch req.Logical {
			case "working", "idle", "waiting", "no_activity", "unknown":
			default:
				sessionFailure(conn, "invalid", ErrInvalid)
				return
			}
		default:
			sessionFailure(conn, "invalid", ErrInvalid)
			return
		}
		at := time.Now()
		if req.At != "" {
			at, err = time.Parse(time.RFC3339Nano, req.At)
			if err != nil {
				sessionFailure(conn, "invalid", err)
				return
			}
		}
		if err = m.SessionEvent(req.AgentID, req.Kind, req.Logical, at); err != nil {
			sessionFailure(conn, "invalid", err)
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{}})
	case "session.ack":
		if err = m.SessionAck(req.AgentID, req.Offset); err != nil {
			sessionFailure(conn, "invalid", err)
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{}})
	default:
		sessionFailure(conn, "invalid", ErrInvalid)
	}
}

// The byte offset advances only when the client explicitly acks. A reconnect
// starts at that durable offset; no wake is consumed merely by disconnecting.
func servePendingSubscribe(ctx context.Context, conn net.Conn, m sessionManager, pid int) {
	start := sup.SessionProcessStart(pid)
	if start == "" {
		sessionFailure(conn, "invalid", fmt.Errorf("session process is not alive"))
		return
	}
	ready, stop := m.AwaitAttachment(pid)
	defer stop()
	if err := json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{}}); err != nil {
		return
	}
	gone := make(chan struct{})
	go func() { var b [1]byte; _, _ = conn.Read(b[:]); close(gone) }()
	find := func() (sup.AgentDefinition, bool) {
		list, _ := mListAttached(m)
		for _, d := range list {
			if d.SessionPID == pid && d.SessionStart == start {
				return d, true
			}
		}
		return sup.AgentDefinition{}, false
	}
	for {
		if d, found := find(); found {
			serveWakeStream(ctx, conn, m, d, gone)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case <-ready:
		case <-time.After(5 * time.Second):
			if !sup.SessionProcessAlive(pid, start) {
				return
			}
		}
	}
}
func mListAttached(m sessionManager) ([]sup.AgentDefinition, error) {
	l, ok := m.(interface {
		List(context.Context) ([]sup.AgentStatus, error)
	})
	if !ok {
		return nil, ErrInvalid
	}
	statuses, err := l.List(context.Background())
	if err != nil {
		return nil, err
	}
	var defs []sup.AgentDefinition
	for _, s := range statuses {
		if s.Kind == "attached" {
			if d, yes := m.Attached(s.AgentID); yes {
				defs = append(defs, d)
			}
		}
	}
	return defs, nil
}
func serveSessionWakes(ctx context.Context, conn net.Conn, m sessionManager, d sup.AgentDefinition) {
	if err := json.NewEncoder(conn).Encode(Response{OK: true, Data: map[string]any{}}); err != nil {
		return
	}
	serveWakeStream(ctx, conn, m, d, nil)
}
func serveWakeStream(ctx context.Context, conn net.Conn, m sessionManager, d sup.AgentDefinition, gone <-chan struct{}) {
	if err := m.SessionConnected(d.AgentID); err != nil {
		reason := d.Reason
		if reason == "" {
			reason = err.Error()
		}
		_ = json.NewEncoder(conn).Encode(map[string]any{"type": "detached", "reason": reason})
		return
	}
	wakes, unsubscribe, err := m.SubscribeWakes(d.AgentID)
	if err != nil {
		sessionFailure(conn, "not_found", err)
		return
	}
	defer unsubscribe()
	enc := json.NewEncoder(conn)
	if err = enc.Encode(map[string]any{"type": "connect", "agentId": d.AgentID, "workstream": d.Workstream, "offset": d.Offset}); err != nil {
		return
	}
	path := m.SpoolPath(d.AgentID)
	if gone == nil {
		ch := make(chan struct{})
		gone = ch
		go func() { var one [1]byte; _, _ = conn.Read(one[:]); close(ch) }()
	}
	if d.Program == "other" {
		defer func() {
			if ctx.Err() == nil {
				current, ok := m.Attached(d.AgentID)
				if ok && current.State != "stopped-by-dashboard" && current.SessionPID == d.SessionPID && current.SessionStart == d.SessionStart {
					_ = m.Detach(d.AgentID, "no listener")
				}
			}
		}()
	}
	offset := d.Offset
	for {
		f, e := os.Open(path)
		if e == nil {
			if _, e = f.Seek(offset, io.SeekStart); e == nil {
				r := bufio.NewReader(f)
				for {
					line, readErr := r.ReadBytes('\n')
					if readErr != nil {
						break
					}
					text, parseErr := WakeSummary(line)
					if parseErr != nil {
						_ = f.Close()
						sessionFailure(conn, "invalid", parseErr)
						return
					}
					offset += int64(len(line))
					if err = enc.Encode(map[string]any{"type": "wake", "offset": offset, "line": text}); err != nil {
						_ = f.Close()
						return
					}
				}
			}
			_ = f.Close()
		} else if !os.IsNotExist(e) {
			sessionFailure(conn, "internal", e)
			return
		}
		// Poll as a fallback for notifications appended while a subscriber was
		// between opening the spool and waiting for its signal.
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case <-wakes:
		case <-time.After(time.Second):
		}
		current, ok := m.Attached(d.AgentID)
		if ok && current.Workstream != d.Workstream {
			d = current
			if err = enc.Encode(map[string]any{"type": "connect", "agentId": d.AgentID, "workstream": d.Workstream, "offset": offset}); err != nil {
				return
			}
		}
		if !ok || current.State == "stopped-by-dashboard" || current.State == "stopped" {
			reason := "stopped from the dashboard"
			if ok && current.Reason != "" {
				reason = current.Reason
			}
			_ = enc.Encode(map[string]any{"type": "detached", "reason": reason})
			return
		}
	}
}

// WakeSummary renders only the server-generated pointer, never a body.
func WakeSummary(line []byte) (string, error) {
	var n agentapi.SpooledNotification
	if err := json.Unmarshal(line, &n); err != nil {
		return "", err
	}
	return strings.TrimSpace(n.Summary), nil
}

var _ = io.EOF
