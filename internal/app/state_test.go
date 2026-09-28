package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStateAdapterTransport(t *testing.T) {
	credential := testCredential()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPut || r.URL.Path != "/agent/v1/workstreams/694/agents/me/state" {
			t.Errorf("wrong route: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Error("missing agent authorization")
		}
		var body struct{ State, Source, At string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.State != "working" || body.Source != "pi" {
			t.Errorf("unexpected state: %+v", body)
		}
		if _, err := time.Parse(time.RFC3339Nano, body.At); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, _, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, credential)
	if code := client.Run([]string{"state", "--workstream", "694", "--agent", credential.AgentID, "--source", "pi", "working"}); code != 0 {
		t.Fatalf("state failed: %s", stderr.String())
	}
	if calls != 1 {
		t.Fatalf("got %d calls, want one", calls)
	}
	if code := client.Run([]string{"state", "--workstream", "694", "--agent", credential.AgentID, "--source", "pi", "unknown"}); code == 0 || calls != 1 {
		t.Fatal("invalid state reached transport")
	}
}
