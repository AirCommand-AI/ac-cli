package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentRoleSetsAndClears(t *testing.T) {
	const id = "agm_12345678901234567890123456789012"
	var bodies []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents":
			_, _ = w.Write([]byte(`{"agents":[{"agentId":"` + id + `","name":"eng-1","status":"active"}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/agent/v1/machines/me/agents/"+id+"/role":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, body)
			label := map[string]string{"reviewer": "Reviewer"}[body["role"]]
			_, _ = w.Write([]byte(`{"agentId":"` + id + `","role":"` + body["role"] + `","roleLabel":"` + label + `"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", nil)
	storedMachine(t, client)
	cases := []struct {
		role, want, sent string
	}{
		{"reviewer", "eng-1 is now Reviewer.", "reviewer"},
		{"none", "eng-1 has no role.", ""},
	}
	for _, tc := range cases {
		code, out, errText := run(t, client, stdout, stderr, "agent", "role", "eng-1", tc.role)
		if code != 0 || !strings.Contains(out, tc.want) {
			t.Fatalf("role %s: %d %q %q", tc.role, code, out, errText)
		}
		if got := bodies[len(bodies)-1]["role"]; got != tc.sent {
			t.Fatalf("sent role %q, want %q", got, tc.sent)
		}
	}
	if code, _, _ := run(t, client, stdout, stderr, "agent", "role", "eng-1", "boss"); code == 0 {
		t.Fatal("unknown role accepted")
	}
	if len(bodies) != 2 {
		t.Fatalf("unknown role reached the server: %v", bodies)
	}
}
