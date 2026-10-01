package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestHTTPPollerStallRoutesUseAgentCredential(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" {
			t.Errorf("missing agent credential: %s", r.Header.Get("Authorization"))
		}
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.RequestURI() {
		case "/agent/v1/workstreams/980":
			_, _ = w.Write([]byte(`{"tasks":[{"id":"other","assignee":"other","status":"in_flight"},{"id":"task-2","assignee":"agm_1","status":"in_flight","number":2,"position":2},{"id":"task-1","assignee":"agm_1","status":"in_flight","number":1,"position":1}]}`))
		case "/agent/v1/workstreams/980/approvals/requests?mine=pending":
			_, _ = w.Write([]byte(`{"requests":[{"id":"approval-1"}]}`))
		case "/agent/v1/workstreams/980/messages/0123456789abcdef":
			_, _ = w.Write([]byte(`{"id":"0123456789abcdef","body":"interrupt now"}`))
		case "/agent/v1/workstreams/980/agents/me/state":
			var state map[string]string
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				t.Error(err)
			}
			if state["state"] != "stalled" || state["reason"] != "tool shell silent 15m" || state["source"] != "daemon" || state["at"] == "" {
				t.Errorf("state payload: %+v", state)
			}
			_, _ = w.Write([]byte(`{}`))
		case "/agent/v1/workstreams/980/updates":
			var update map[string]string
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Error(err)
			}
			if update["taskId"] != "task-1" || !strings.Contains(update["summary"], "#1") || update["idempotencyId"] == "" {
				t.Errorf("update payload: %+v", update)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	store := credentials.NewStore(home)
	d := definition(home)
	d.Workstream = "980"
	if err := store.Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, APIToken: "agent-token", SocketKey: "key", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	p := &HTTPPoller{BaseURL: server.URL, Client: server.Client(), Store: store}
	task, found, pending, err := p.InFlight(context.Background(), d)
	if err != nil || !found || !pending || task.ID != "task-1" {
		t.Fatalf("task %v %v %v %v", task, found, pending, err)
	}
	if err := p.StateReason(context.Background(), d, "stalled", "tool shell silent 15m"); err != nil {
		t.Fatal(err)
	}
	body, err := p.MessageBody(context.Background(), d, "0123456789abcdef")
	if err != nil || body != "interrupt now" {
		t.Fatalf("body %q %v", body, err)
	}
	if err := p.NudgeUpdate(context.Background(), d, task); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 {
		t.Fatalf("requests: %v", requests)
	}
}
