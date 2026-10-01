package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewCommandFlow(t *testing.T) {
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(structuredDetail))
			return
		}
		writes = append(writes, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"finding-1"}`))
	}))
	defer server.Close()
	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x5a))
	saveTestCredential(t, client, testCredential())
	commands := [][]string{
		{"review", "start", "5", "--workstream", "694", "--of", "1"},
		{"review", "finding", "5", "--workstream", "694", "--severity", "major", "--category", "correctness", "--summary", "Missing guard", "--file", "api.go", "--line", "9"},
		{"review", "finish", "5", "--workstream", "694", "--outcome", "sent_back"},
		{"review", "finding-status", "finding-1", "--workstream", "694", "--status", "fixed"},
	}
	for _, args := range commands {
		if client.Run(args) != 0 {
			t.Fatalf("%v: %s", args, stderr.String())
		}
	}
	if len(writes) != 4 || !strings.HasSuffix(writes[0], "/reviews/aaaaaaaaaaaaaaa5/start") || !strings.HasSuffix(writes[1], "/reviews/aaaaaaaaaaaaaaa5/findings") || !strings.HasSuffix(writes[2], "/reviews/aaaaaaaaaaaaaaa5/finish") || !strings.HasSuffix(writes[3], "/findings/finding-1") {
		t.Fatalf("review writes %v", writes)
	}
	if client.Run([]string{"review", "finding", "5", "--workstream", "694", "--severity", "severe", "--category", "correctness", "--summary", "x"}) == 0 || len(writes) != 4 {
		t.Fatal("unknown severity accepted")
	}
}

// The server's reason must reach the reviewer. Every 404 used to print
// "Workstream N was not found", which hid "Review or task not found" (and read
// like an HTTP code when the workstream was itself numbered 404).
func TestReviewErrorsShowServerReason(t *testing.T) {
	cases := []struct {
		name, action string
		status       int
		body         string
		want         []string
		notWant      string
	}{
		{"not found", "start", http.StatusNotFound, `{"message":"Review or task not found","code":"NotFound"}`,
			[]string{"Review start failed: Review or task not found (HTTP 404).", "own task of type review", "--of"}, "Workstream 694 was not found"},
		{"wrong task type", "start", http.StatusBadRequest, `{"message":"review task must have type review","code":"BadRequest"}`,
			[]string{"Review start failed: review task must have type review (HTTP 400).", "task create --type review"}, "request failed"},
		{"not reviewer", "finish", http.StatusForbidden, `{"message":"Only the reviewer may finish the review","code":"Forbidden"}`,
			[]string{"Review finish failed: Only the reviewer may finish the review (HTTP 403)."}, "own task of type review"},
		{"no message keeps workstream wording", "start", http.StatusNotFound, `{}`,
			[]string{"Workstream 694 was not found"}, "Review start failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(structuredDetail))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x5a))
			saveTestCredential(t, client, testCredential())
			args := []string{"review", tc.action, "5", "--workstream", "694", "--of", "1"}
			if tc.action == "finish" {
				args = []string{"review", "finish", "5", "--workstream", "694", "--outcome", "approved"}
			}
			if client.Run(args) == 0 {
				t.Fatal("review error not reported")
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("missing %q in %q", want, stderr.String())
				}
			}
			if strings.Contains(stderr.String(), tc.notWant) {
				t.Fatalf("unexpected %q in %q", tc.notWant, stderr.String())
			}
		})
	}
}
