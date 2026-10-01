package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApprovalRequestUsesOnlyAgentCredentialAndExpectedScope(t *testing.T) {
	credential := testCredential()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != "/agent/v1/workstreams/694/approvals/requests" || r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Errorf("wrong request: %s %s", r.Method, r.URL.String())
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["action"] != "deploy.prod" || body["task"] != "#21" || body["note"] != "ship" {
			t.Errorf("body = %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"requestId":"abc","agentId":"` + credential.AgentID + `","status":"pending"}`))
	}))
	defer server.Close()
	a, out, errOut := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, credential)
	if code := a.Run([]string{"approval", "request", "--workstream", "694", "--agent", credential.AgentID, "--action", "deploy.prod", "--task", "#21", "--note", "ship"}); code != 0 {
		t.Fatalf("request code %d: %s", code, errOut.String())
	}
	if requests != 1 || !strings.Contains(out.String(), "Approval requested: abc") {
		t.Fatalf("requests=%d output=%s", requests, out.String())
	}
}
func TestApprovalCheckOnlyTrustsHumanActiveScopedUnexpiredGrant(t *testing.T) {
	credential := testCredential()
	grant := approvalGrant{ID: "f00d", Grantee: credential.AgentID, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Status: "active"}
	grant.GrantedBy.ID = "usr_owner"
	grant.GrantedBy.Nature = "human"
	grant.GrantedBy.Name = "Owner"
	grant.Scope.Actions = []string{"work.start"}
	status := 200
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/agent/v1/workstreams/694/approvals/check" || r.URL.Query().Get("action") != "work.start" || r.URL.Query().Get("task") != "21" || r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Errorf("wrong request: %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(status)
		if status == 200 {
			_ = json.NewEncoder(w).Encode(grant)
		}
	}))
	defer server.Close()
	a, out, errOut := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, credential)
	args := []string{"approval", "check", "--workstream", "694", "--action", "work.start", "--task", "21"}
	if got := a.Run(args); got != 0 || !strings.Contains(out.String(), "grant f00d") || !strings.Contains(out.String(), "Owner") {
		t.Fatalf("check=%d output=%q err=%q", got, out.String(), errOut.String())
	}
	for _, tc := range []struct {
		name  string
		alter func()
	}{{"agent issuer", func() { grant.GrantedBy.Nature = "agent" }}, {"expired", func() {
		grant.GrantedBy.Nature = "human"
		grant.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	}}, {"revoked", func() { grant.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano); grant.Status = "revoked" }}, {"wrong action", func() { grant.Status = "active"; grant.Scope.Actions = []string{"deploy.prod"} }}} {
		tc.alter()
		out.Reset()
		errOut.Reset()
		if got := a.Run(args); got == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "Do not proceed") {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", tc.name, got, out.String(), errOut.String())
		}
	}
	status = 403
	out.Reset()
	errOut.Reset()
	if got := a.Run(args); got == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "No approval") {
		t.Fatalf("missing grant: exit=%d out=%q err=%q", got, out.String(), errOut.String())
	}
	if calls != 6 {
		t.Fatalf("calls = %d, want 6", calls)
	}
}
func TestApprovalRejectsInvalidFlagsBeforeRequest(t *testing.T) {
	a, _, stderr := testApp(t, "http://127.0.0.1:1", "", nil)
	for _, args := range [][]string{{"approval", "check", "--workstream", "694"}, {"approval", "check", "--workstream", "694", "--action", "deploy.prod", "--note", "not allowed"}, {"approval", "request", "--workstream", "694", "--action", "deploy.prod", "extra"}} {
		stderr.Reset()
		if a.Run(args) == 0 || !strings.Contains(stderr.String(), "Usage: aircom approval") {
			t.Fatalf("args %v: %q", args, stderr.String())
		}
	}
}

// An invented action must say which actions exist, not just print usage:
// an agent asked for "git.push" and stalled waiting for a grant that cannot exist.
func TestApprovalUnknownActionListsValidActions(t *testing.T) {
	a, _, stderr := testApp(t, "http://127.0.0.1:1", "", nil)
	for _, action := range []string{"git.push", "made.up"} {
		for _, operation := range []string{"check", "request"} {
			t.Run(operation+" "+action, func(t *testing.T) {
				stderr.Reset()
				if a.Run([]string{"approval", operation, "--workstream", "694", "--action", action}) == 0 {
					t.Fatal("unknown action accepted")
				}
				for _, want := range append([]string{`Unknown approval action "` + action + `"`, "pushing feature branches"}, approvalActionOrder...) {
					if !strings.Contains(stderr.String(), want) {
						t.Fatalf("missing %q in %q", want, stderr.String())
					}
				}
			})
		}
	}
	if len(approvalActionOrder) != len(approvalActions) {
		t.Fatalf("approvalActionOrder %v does not match approvalActions", approvalActionOrder)
	}
	for _, action := range approvalActionOrder {
		if !approvalActions[action] {
			t.Fatalf("approvalActionOrder lists unknown action %q", action)
		}
	}
}
