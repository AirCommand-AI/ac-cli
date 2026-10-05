package daemonclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SessionAttach identifies the process that owns a manually attached agent.
// The daemon checks peer ancestry; a caller cannot attach an unrelated pid.
type SessionAttach struct {
	AgentID      string `json:"agentId"`
	Workstream   string `json:"workstream"`
	SessionPID   int    `json:"sessionPid"`
	SessionStart string `json:"sessionStart"`
	Program      string `json:"program"`
	SessionID    string `json:"sessionId,omitempty"`
}

type SessionMessage struct {
	Type       string `json:"type"`
	AgentID    string `json:"agentId,omitempty"`
	Workstream string `json:"workstream,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Line       string `json:"line,omitempty"`
	Text       string `json:"text,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type SessionLookup struct {
	AgentID    string `json:"agentId"`
	Workstream string `json:"workstream"`
}

func (c Client) Claim(ctx context.Context, agentID, workstream string) error {
	return c.call(ctx, map[string]any{"op": "agent.claim", "agentId": agentID, "workstream": workstream}, nil)
}

// ClaimSession keeps the claiming connection alive across the remote Join and
// local Attach. The daemon releases an un-attached claim when this closes.
func (c Client) ClaimSession(ctx context.Context, agentID, workstream string) (io.Closer, error) {
	if c.SocketPath == "" {
		return nil, errors.New("daemon socket path is empty")
	}
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]any{"op": "agent.claim", "agentId": agentID, "workstream": workstream}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var response struct {
		OK    bool         `json:"ok"`
		Error *RemoteError `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !response.OK {
		_ = conn.Close()
		if response.Error != nil {
			return nil, response.Error
		}
		return nil, errors.New("daemon returned invalid claim response")
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
func (c Client) AttachSession(ctx context.Context, r SessionAttach) error {
	return c.call(ctx, struct {
		Op string `json:"op"`
		SessionAttach
	}{"session.attach", r}, nil)
}
func (c Client) DetachSession(ctx context.Context, agentID string) error {
	return c.call(ctx, map[string]any{"op": "session.detach", "agentId": agentID}, nil)
}
func (c Client) SessionEvent(ctx context.Context, pid int, kind, logical string) error {
	request := map[string]any{"op": "session.event", "sessionPid": pid, "kind": kind, "at": time.Now().UTC().Format(time.RFC3339Nano)}
	if logical != "" {
		request["logical"] = logical
	}
	return c.call(ctx, request, nil)
}
func (c Client) AckSession(ctx context.Context, pid int, offset int64) error {
	return c.call(ctx, map[string]any{"op": "session.ack", "sessionPid": pid, "offset": offset}, nil)
}
func (c Client) LookupSession(ctx context.Context, sessionID string) (SessionLookup, error) {
	var result SessionLookup
	err := c.call(ctx, map[string]any{"op": "session.lookup", "sessionId": sessionID}, &result)
	return result, err
}

// SubscribeSession holds one local socket open. The daemon's normal response
// envelope precedes streaming event lines. Cancellation closes the connection
// so a restarted daemon can be retried by the caller without a stuck reader.
func (c Client) SubscribeSession(ctx context.Context, pid int, handle func(SessionMessage) error) error {
	if c.SocketPath == "" {
		return errors.New("daemon socket path is empty")
	}
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"op": "session.subscribe", "sessionPid": pid}); err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	reader := bufio.NewReader(conn)
	decoder := json.NewDecoder(reader)
	// The first line is the usual daemon response, not a notification.
	var response struct {
		OK    bool         `json:"ok"`
		Error *RemoteError `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("subscribe response: %w", err)
	}
	if !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return errors.New("daemon returned an invalid subscribe response")
	}
	for {
		var msg SessionMessage
		if err := decoder.Decode(&msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return fmt.Errorf("read daemon session stream: %w", err)
		}
		switch msg.Type {
		case "connect", "wake", "nudge", "interrupt", "detached":
		default:
			return fmt.Errorf("invalid session message type %q", msg.Type)
		}
		if err := handle(msg); err != nil {
			return err
		}
		if msg.Type == "detached" {
			return nil
		}
	}
}
