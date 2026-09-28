package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsRoundTrip(t *testing.T) {
	credential := testCredential()
	rev := 0
	body := ""
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Errorf("wrong authentication")
		}
		if !strings.HasPrefix(r.URL.Path, "/agent/v1/workstreams/694/docs") {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/diff"):
			if r.URL.Query().Get("from") != "1" {
				t.Errorf("missing from: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"diff":"--- design@1\n+++ design@2\n"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/design"):
			fmt.Fprintf(w, `{"body":%q,"revision":{"rev":%d}}`, body, rev)
		case r.Method == http.MethodPut:
			var input struct {
				BodyBase64 string `json:"bodyBase64"`
				BaseRev    int    `json:"baseRev"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.BaseRev != rev {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"code":"DocStale","message":"doc changed since rev"}`)
				return
			}
			content, err := base64.StdEncoding.DecodeString(input.BodyBase64)
			if err != nil {
				t.Error(err)
			}
			body = string(content)
			rev++
			fmt.Fprintf(w, `{"rev":%d}`, rev)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/archive"):
			fmt.Fprint(w, `{"archived":true}`)
		default:
			fmt.Fprint(w, `{"docs":[{"name":"design","latestRev":1}]}`)
		}
	}))
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, credential)
	path := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(path, []byte("# Design\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if code := client.Run(args); code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, stderr.String())
		}
	}
	run("docs", "--workstream", "694")
	if !strings.Contains(stdout.String(), "design") {
		t.Fatal(stdout.String())
	}
	stdout.Reset()
	run("doc", "put", "design", "--workstream", "694", "--file", path, "--base-rev", "0")
	stdout.Reset()
	run("doc", "get", "design", "--workstream", "694")
	if stdout.String() != "# Design\n" || !strings.Contains(stderr.String(), "rev 1") {
		t.Fatal(stdout.String(), stderr.String())
	}
	stdout.Reset()
	run("doc", "diff", "design", "--workstream", "694", "--from", "1")
	if !strings.Contains(stdout.String(), "--- design@1") {
		t.Fatal(stdout.String())
	}
	stdout.Reset()
	run("doc", "archive", "design", "--workstream", "694")
	if requests != 5 {
		t.Fatal(requests)
	}
	if code := client.Run([]string{"doc", "put", "design", "--workstream", "694", "--file", path, "--base-rev", "0"}); code == 0 || !strings.Contains(stderr.String(), "doc changed since rev 0; fetch and merge") {
		t.Fatal("stale write accepted or message missing", stderr.String())
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", docLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	count := requests
	if code := client.Run([]string{"doc", "put", "design", "--workstream", "694", "--file", path, "--base-rev", "1"}); code == 0 || requests != count {
		t.Fatal("oversized file reached API")
	}
}
