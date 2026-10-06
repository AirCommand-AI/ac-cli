package runmode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

type stopFunc func(context.Context) error

func (f stopFunc) BeginRunFinishing(ctx context.Context) error { return f(ctx) }
func TestAuthWatcherUploadsOnlyChangedOpenAICodexEntry(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
	path := filepath.Join(home, ".pi", "agent", "auth.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(`{"openai-codex":{"refresh":"first"},"other":{"key":"never"}}`), 0o600)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Login map[string]any `json:"login"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Login) != 1 || body.Login["other"] != nil {
			t.Errorf("private login leak: %+v", body)
		}
		calls.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	service := Service{Home: home, API: API{BaseURL: server.URL, Client: server.Client(), Store: store}, LoginInterval: time.Millisecond}
	go func() { service.WatchLogin(ctx); close(done) }()
	time.Sleep(15 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("initial login was re-uploaded: %d", calls.Load())
	}
	_ = os.WriteFile(path, []byte(`{"openai-codex":{"refresh":"second"},"other":{"key":"never"}}`), 0o600)
	for i := 0; i < 100 && calls.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if calls.Load() != 1 {
		t.Fatalf("changed login uploads=%d", calls.Load())
	}
}

func TestFinishingRenewsBeforeStopAndReportsRescueAndLogin(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
	auth := filepath.Join(home, ".pi", "agent", "auth.json")
	_ = os.MkdirAll(filepath.Dir(auth), 0o700)
	_ = os.WriteFile(auth, []byte(`{"openai-codex":{"access":"token"},"other":{"apiKey":"must-not-upload"}}`), 0o600)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "github-token"):
			_, _ = fmt.Fprint(w, `{"token":"fresh-github-token","expiresAt":"2030-10-06T20:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "chatgpt-login"):
			var body struct {
				Login map[string]any `json:"login"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Login) != 1 || body.Login["other"] != nil {
				t.Errorf("leaked other login: %+v", body)
			}
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "finished"):
			var body struct {
				Rescue        []RescueResult `json:"rescue"`
				LoginUploaded bool           `json:"loginUploaded"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !body.LoginUploaded || len(body.Rescue) != 0 {
				t.Errorf("finished payload: %+v", body)
			}
			w.WriteHeader(204)
		default:
			t.Errorf("path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	api := API{BaseURL: server.URL, Client: server.Client(), Store: store}
	now := time.Now()
	service := Service{Home: home, API: api, Tokens: &TokenSource{API: api, Now: func() time.Time { return now }}, Now: func() time.Time { return now }, Stop: stopFunc(func(context.Context) error { calls = append(calls, "stop"); return nil }), Clones: func() []Clone { return nil }}
	deadline := now.Add(5 * time.Minute)
	if err := service.Finish(context.Background(), RunStatus{RunID: "run_test", State: "finishing", RescueDeadline: &deadline}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || !strings.HasSuffix(calls[0], "github-token") || calls[1] != "stop" || !strings.HasSuffix(calls[2], "chatgpt-login") || !strings.HasSuffix(calls[3], "finished") {
		t.Fatalf("order %v", calls)
	}
}
