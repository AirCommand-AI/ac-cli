package runmode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestTokenSourceCachesThenRenewsAndCredentialHelperGuardsHost(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ac-r-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/machines/me/run/github-token" || r.Header.Get("Authorization") != "Bearer machine" {
			t.Errorf("request %s bearer %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		calls++
		_, _ = fmt.Fprintf(w, `{"token":"token-%d","expiresAt":"2030-10-06T20:00:00Z"}`, calls)
	}))
	defer server.Close()
	now := time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)
	tokens := &TokenSource{API: API{BaseURL: server.URL, Client: server.Client(), Store: store}, Now: func() time.Time { return now }}
	if got, err := tokens.Get(context.Background(), false); err != nil || got != "token-1" {
		t.Fatalf("token %s %v", got, err)
	}
	if got, err := tokens.Get(context.Background(), false); err != nil || got != "token-1" || calls != 1 {
		t.Fatalf("cache %s %v calls=%d", got, err, calls)
	}
	now = now.Add(51 * time.Minute)
	if got, err := tokens.Get(context.Background(), false); err != nil || got != "token-2" || calls != 2 {
		t.Fatalf("renewal %s %v calls=%d", got, err, calls)
	}
	info, err := os.Stat(filepath.Join(home, ".config", "gh", "hosts.yml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("gh token not private: %v %+v", err, info)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	go func() { close(ready); _ = ServeTokens(ctx, home, tokens) }()
	<-ready
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(TokenSocket(home)); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	var out bytes.Buffer
	if err := GitCredential(ctx, home, strings.NewReader("protocol=https\nhost=evil.example\n\n"), &out); err != nil || out.Len() != 0 {
		t.Fatalf("foreign host: %q %v", out.String(), err)
	}
	if err := GitCredential(ctx, home, strings.NewReader("protocol=https\nhost=github.com\n\n"), &out); err != nil || !strings.Contains(out.String(), "password=token-2") || strings.Contains(out.String(), "machine") {
		t.Fatalf("credential: %q %v", out.String(), err)
	}
}
func TestRunAPIStatusAndLoginPayload(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"})
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/agent/v1/machines/me/run" {
			_, _ = w.Write([]byte(`{"runId":"run_test","state":"finishing","hardLimitAt":"2030-01-01T00:00:00Z"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "chatgpt-login") {
			var body struct {
				Login map[string]any `json:"login"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Login) != 1 || body.Login["openai-codex"] == nil {
				t.Errorf("bad login %+v", body)
			}
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	api := API{BaseURL: server.URL, Client: server.Client(), Store: store}
	status, err := api.Status(context.Background())
	if err != nil || status.State != "finishing" {
		t.Fatalf("status %+v %v", status, err)
	}
	if err := api.UploadLogin(context.Background(), json.RawMessage(`{"openai-codex":{"type":"oauth"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := api.Finished(context.Background(), []RescueResult{{Agent: "eng-1", Result: "nothing"}}, true); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatal(paths)
	}
}
