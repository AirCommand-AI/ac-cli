package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/gorilla/websocket"
)

type fakeSink struct {
	mu       sync.Mutex
	seen     []agentapi.Notification
	catchups int
	wake     chan struct{}
}

func (f *fakeSink) Wake(_ context.Context, _ string, n agentapi.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, n)
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}
func (f *fakeSink) CatchUp(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catchups++
	return nil
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestMachineSocketFixtures(t *testing.T) {
	sink := &fakeSink{wake: make(chan struct{}, 2)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/machine" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("handshake %s %s", r.URL.Path, r.Header.Get("Authorization"))
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, fixture(t, "hello.json"))
		_ = c.WriteMessage(websocket.TextMessage, fixture(t, "wake.json"))
		_ = c.WriteMessage(websocket.TextMessage, fixture(t, "wake-minimal.json"))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &SocketClient{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/machine", Secret: "secret", MachineID: "device_123", Sink: sink}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-sink.wake:
		case <-time.After(2 * time.Second):
			t.Fatal("wake not received")
		}
	}
	state := c.State()
	if !state.Connected || state.Node != "node-1" || state.Generation != 7 || state.Since == "" {
		t.Fatalf("status %+v", state)
	}
	sink.mu.Lock()
	if sink.catchups != 1 || len(sink.seen) != 2 || sink.seen[0].MessageID != "msg_123" || sink.seen[1].Priority != "urgent" {
		t.Errorf("wakes %+v, catchups %d", sink.seen, sink.catchups)
	}
	sink.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket did not exit")
	}
}
func TestSocketPingAndReconnectCatchUp(t *testing.T) {
	sink := &fakeSink{wake: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	connects := 0
	ping := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		mu.Lock()
		connects++
		n := connects
		mu.Unlock()
		_ = c.WriteMessage(websocket.TextMessage, fixture(t, "hello.json"))
		if n == 1 {
			_ = c.Close()
			return
		}
		for {
			var frame struct {
				Type string `json:"type"`
			}
			if err := c.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Type == "ping" {
				select {
				case ping <- struct{}{}:
				default:
				}
				_ = c.WriteJSON(map[string]string{"type": "pong"})
			}
		}
	}))
	defer server.Close()
	client := &SocketClient{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Secret: "secret", MachineID: "device_123", Sink: sink, PingInterval: 5 * time.Millisecond, Backoff: func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-ping:
	case <-time.After(2 * time.Second):
		t.Fatal("ping not sent after reconnect")
	}
	sink.mu.Lock()
	got := sink.catchups
	sink.mu.Unlock()
	if got != 2 {
		t.Fatalf("catchups %d, want 2", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket did not stop")
	}
}
func TestMachineSocketTerminal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"revoked", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }, ErrSocketRevoked},
		{"superseded", func(w http.ResponseWriter, r *http.Request) {
			c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			_ = c.WriteMessage(websocket.TextMessage, fixture(t, "hello.json"))
			var closeFrame struct {
				Code   int    `json:"code"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(fixture(t, "superseded-close.json"), &closeFrame)
			_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(closeFrame.Code, closeFrame.Reason), time.Now().Add(time.Second))
		}, ErrSocketSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client := &SocketClient{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Secret: "secret", MachineID: "device_123", Sink: &fakeSink{wake: make(chan struct{}, 1)}, Backoff: func(context.Context, time.Duration) bool { t.Fatal("terminal socket error retried"); return false }}
			if err := client.Run(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
