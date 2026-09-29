package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/gorilla/websocket"
)

// WakeSink is implemented by supervisor.Manager. The socket never writes the
// notification cursor; CatchUp and periodic HTTP polling alone may advance it.
type WakeSink interface {
	Wake(context.Context, string, agentapi.Notification) error
	CatchUp(context.Context) error
}
type SocketState struct {
	Connected  bool   `json:"connected"`
	Since      string `json:"since,omitempty"`
	Node       string `json:"node,omitempty"`
	Generation int    `json:"generation,omitempty"`
}
type SocketClient struct {
	URL, Secret, MachineID string
	SecretLoader           func(context.Context) (string, error)
	Sink                   WakeSink
	Dialer                 *websocket.Dialer
	Backoff                func(context.Context, time.Duration) bool
	PingInterval           time.Duration
	mu                     sync.RWMutex
	state                  SocketState
}

func (c *SocketClient) State() SocketState         { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }
func (c *SocketClient) setState(state SocketState) { c.mu.Lock(); defer c.mu.Unlock(); c.state = state }
func socketWait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var (
	ErrSocketRevoked    = errors.New("machine socket secret revoked (HTTP 401)")
	ErrSocketSuperseded = errors.New("machine socket superseded (4001)")
)

// Run reconnects after transient errors, but never after revocation or a
// server-owned generation has superseded this connection.
func (c *SocketClient) Run(ctx context.Context) error {
	if c.Sink == nil || c.URL == "" || (c.Secret == "" && c.SecretLoader == nil) {
		return errors.New("machine socket is not configured")
	}
	wait := c.Backoff
	if wait == nil {
		wait = socketWait
	}
	for attempt := 0; ctx.Err() == nil; attempt++ {
		var err error
		if c.Secret == "" && c.SecretLoader != nil {
			c.Secret, err = c.SecretLoader(ctx)
		}
		if err == nil {
			err = c.connect(ctx)
		}
		c.setState(SocketState{})
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrSocketRevoked) || errors.Is(err, ErrSocketSuperseded) {
			return err
		}
		delay := time.Second << min(attempt, 6)
		if !wait(ctx, delay) {
			return nil
		}
	}
	return nil
}
func (c *SocketClient) connect(ctx context.Context) error {
	dialer := c.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	headers := http.Header{"Authorization": []string{"Bearer " + c.Secret}}
	conn, response, err := dialer.DialContext(ctx, c.URL, headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return ErrSocketRevoked
			}
		}
		return err
	}
	defer conn.Close()
	// Cancel a blocked ReadMessage promptly on service shutdown.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	conn.SetReadLimit(64 * 1024)
	var hello struct {
		Type       string `json:"type"`
		MachineID  string `json:"machineId"`
		Generation int    `json:"generation"`
		Node       string `json:"node"`
	}
	if err := conn.ReadJSON(&hello); err != nil {
		return err
	}
	if hello.Type != "hello" || hello.MachineID != c.MachineID || hello.Generation < 1 || hello.Node == "" {
		return errors.New("invalid machine socket hello")
	}
	c.setState(SocketState{Connected: true, Since: time.Now().UTC().Format(time.RFC3339Nano), Node: hello.Node, Generation: hello.Generation})
	if ready, ok := c.Sink.(interface{ WaitReady(context.Context) error }); ok {
		if err := ready.WaitReady(ctx); err != nil {
			return err
		}
	}
	if err := c.Sink.CatchUp(ctx); err != nil {
		return err
	}
	pingCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval := c.PingInterval
	if interval <= 0 {
		interval = 20 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteJSON(map[string]string{"type": "ping"}); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		kind, body, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code == 4001 {
				return ErrSocketSuperseded
			}
			return err
		}
		if kind != websocket.TextMessage {
			continue
		}
		var frame struct {
			Type         string                `json:"type"`
			AgentID      string                `json:"agentId"`
			Notification agentapi.Notification `json:"notification"`
		}
		if err := json.Unmarshal(body, &frame); err != nil {
			return fmt.Errorf("invalid machine socket frame: %w", err)
		}
		if frame.Type == "pong" {
			continue
		}
		if frame.Type != "wake" || frame.AgentID == "" {
			return errors.New("invalid machine socket wake")
		}
		if err := c.Sink.Wake(ctx, frame.AgentID, frame.Notification); err != nil {
			return err
		}
	}
}
