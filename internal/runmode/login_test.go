package runmode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func passServer(t *testing.T, status *atomic.Int32, calls *atomic.Int32, expires time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/machines/me/run/chatgpt-token" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer machine" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		n := calls.Add(1)
		if code := int(status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"ChatGPT login needs sign-in","reason":"needs_sign_in"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access": "access-" + string(rune('0'+n)), "accountId": "chat", "expires": expires.UnixMilli()})
	}))
}

func readPass(t *testing.T, home string) (map[string]json.RawMessage, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	var entry map[string]any
	if json.Unmarshal(data, &file) != nil || json.Unmarshal(file["openai-codex"], &entry) != nil {
		t.Fatalf("auth.json = %s", data)
	}
	return file, entry
}

func TestRefreshPass(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name      string
		installed string
		status    int32
		wantCalls int32
		wantErr   error
		wantNew   bool
	}{
		{"missing login fetches a pass", "", http.StatusOK, 1, nil, true},
		{"fresh pass is kept", `{"openai-codex":{"type":"oauth","access":"old","expires":` + jsonInt(now.Add(3*time.Hour).UnixMilli()) + `},"other":{"key":"keep"}}`, http.StatusOK, 0, nil, false},
		{"expiring pass is replaced", `{"openai-codex":{"type":"oauth","access":"old","expires":` + jsonInt(now.Add(20*time.Minute).UnixMilli()) + `},"other":{"key":"keep"}}`, http.StatusOK, 1, nil, true},
		{"needs sign-in leaves the file alone", `{"openai-codex":{"type":"oauth","access":"old","expires":` + jsonInt(now.Add(5*time.Minute).UnixMilli()) + `}}`, http.StatusConflict, 1, ErrLoginNeedsSignIn, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			store := credentials.NewStore(home)
			_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
			if tt.installed != "" {
				_ = os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700)
				_ = os.WriteFile(filepath.Join(home, ".pi", "agent", "auth.json"), []byte(tt.installed), 0o600)
			}
			var status, calls atomic.Int32
			status.Store(tt.status)
			server := passServer(t, &status, &calls, now.Add(6*time.Hour))
			defer server.Close()
			s := Service{Home: home, API: API{BaseURL: server.URL, Client: server.Client(), Store: store}, LoginManaged: true}
			err := s.refreshPass(context.Background())
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v; want %v", err, tt.wantErr)
			}
			if calls.Load() != tt.wantCalls {
				t.Fatalf("calls = %d; want %d", calls.Load(), tt.wantCalls)
			}
			if tt.installed == "" && !tt.wantNew {
				return
			}
			file, entry := readPass(t, home)
			if tt.wantNew {
				if entry["access"] != "access-1" || entry["refresh"] != managedRefresh || entry["type"] != "oauth" || entry["accountId"] != "chat" {
					t.Fatalf("entry = %v", entry)
				}
			} else if entry["access"] != "old" {
				t.Fatalf("entry changed: %v", entry)
			}
			if tt.installed != "" && len(tt.installed) > 0 && json.Valid([]byte(tt.installed)) {
				var before map[string]json.RawMessage
				_ = json.Unmarshal([]byte(tt.installed), &before)
				if before["other"] != nil && string(file["other"]) != string(before["other"]) {
					t.Fatalf("other providers lost: %s", file["other"])
				}
			}
		})
	}
}

func TestKeepLoginResumesAfterSignIn(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
	var status, calls atomic.Int32
	status.Store(http.StatusConflict)
	server := passServer(t, &status, &calls, time.Now().Add(6*time.Hour))
	defer server.Close()
	var logs []string
	s := Service{Home: home, API: API{BaseURL: server.URL, Client: server.Client(), Store: store}, LoginManaged: true, LoginInterval: 5 * time.Millisecond, OnError: func(err error) { logs = append(logs, err.Error()) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.KeepLogin(ctx); close(done) }()
	for i := 0; i < 200 && calls.Load() < 3; i++ {
		time.Sleep(time.Millisecond)
	}
	status.Store(http.StatusOK)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "auth.json")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	_, entry := readPass(t, home)
	if entry["refresh"] != managedRefresh {
		t.Fatalf("entry = %v", entry)
	}
	if len(logs) < 2 || logs[0] != "agents paused: ChatGPT login needs sign-in on the dashboard" || logs[len(logs)-1] != "ChatGPT login signed in again; agents resume" {
		t.Fatalf("logs = %q (needs-sign-in must be reported once, then the recovery)", logs)
	}
}

func TestFinishDoesNotUploadAManagedLogin(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
	_ = os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".pi", "agent", "auth.json"), []byte(`{"openai-codex":{"access":"a"}}`), 0o600)
	var uploads atomic.Int32
	var finished map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/machines/me/run/chatgpt-login":
			uploads.Add(1)
		case "/agent/v1/machines/me/run/finished":
			_ = json.NewDecoder(r.Body).Decode(&finished)
		case "/agent/v1/machines/me/run/github-token":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expiresAt": time.Now().Add(time.Hour)})
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	api := API{BaseURL: server.URL, Client: server.Client(), Store: store}
	s := Service{Home: home, API: api, Tokens: &TokenSource{API: api}, Stop: stopFunc(func(context.Context) error { return nil }), Clones: func() []Clone { return nil }, LoginManaged: true}
	if err := s.Finish(context.Background(), RunStatus{RunID: "run", State: "finishing"}); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 0 || finished["loginUploaded"] != false {
		t.Fatalf("uploads=%d finished=%v", uploads.Load(), finished)
	}
}

func jsonInt(v int64) string { data, _ := json.Marshal(v); return string(data) }
