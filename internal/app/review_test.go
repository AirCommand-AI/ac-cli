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
