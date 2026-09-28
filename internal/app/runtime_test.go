package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeAdapterTransport(t *testing.T) {
	credential := testCredential()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPut || r.URL.Path != "/agent/v1/workstreams/694/agents/me/runtime" {
			t.Errorf("wrong route: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Error("missing agent authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "m1" || body["harness"] != "pi" {
			t.Errorf("unexpected runtime: %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, _, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, credential)
	if code := client.Run([]string{"runtime", "--workstream", "694", "--agent", credential.AgentID, "--json", `{"harness":"pi","model":"m1"}`}); code != 0 {
		t.Fatalf("runtime failed: %s", stderr.String())
	}
	if calls != 1 {
		t.Fatalf("got %d calls, want one", calls)
	}
	if code := client.Run([]string{"runtime", "--workstream", "694", "--agent", credential.AgentID, "--json", `{invalid`}); code == 0 || calls != 1 {
		t.Fatal("invalid payload reached transport")
	}
}
