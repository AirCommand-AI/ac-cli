package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupScriptsRoundTrip(t *testing.T) {
	cred := testCredential()
	scripts := map[string]setupScript{"ss_rust": {ID: "ss_rust", Name: "Rust + Go", Script: "echo rust\n", UpdatedBy: "Rahul", UpdatedAt: "2026-10-23T10:00:00Z"}}
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cred.APIToken || !strings.HasPrefix(r.URL.Path, "/agent/v1/workstreams/694/setup-scripts") {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/agent/v1/workstreams/694/setup-scripts"), "/")
		var body struct{ Name, Script string }
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			writes = append(writes, fmt.Sprintf("%s %s %s %q", r.Method, id, body.Name, body.Script))
		}
		switch r.Method {
		case http.MethodGet:
			list := []setupScript{}
			for _, s := range scripts {
				list = append(list, s)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"scripts": list})
		case http.MethodPost:
			if body.Name == "Bad" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"Write a script of up to 10000 characters of plain text.","field":"script"}`)
				return
			}
			scripts["ss_py"] = setupScript{ID: "ss_py", Name: body.Name, Script: body.Script}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case http.MethodPut:
			s := scripts[id]
			s.Script = body.Script
			scripts[id] = s
			fmt.Fprint(w, `{}`)
		case http.MethodDelete:
			writes = append(writes, "DELETE "+id)
			delete(scripts, id)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, cred)
	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		if code := client.Run(args); code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, stderr.String())
		}
		return stdout.String()
	}
	if out := run("setup-scripts", "--workstream", "694"); !strings.Contains(out, "Rust + Go\t1 lines\tchanged by Rahul") {
		t.Fatalf("list: %q", out)
	}
	if out := run("setup-script", "get", "rust + go", "--workstream", "694"); out != "echo rust\n" {
		t.Fatalf("get: %q", out)
	}
	file := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(file, []byte("echo rust 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := run("setup-script", "put", "Rust + Go", "--workstream", "694", "--file", file); !strings.Contains(out, "updated: Rust + Go") {
		t.Fatalf("update: %q", out)
	}
	client.Stdin = strings.NewReader("pip install uv\n")
	if out := run("setup-script", "put", "Python", "--workstream", "694"); !strings.Contains(out, "created: Python") {
		t.Fatalf("create: %q", out)
	}
	if strings.Join(writes, "|") != `PUT ss_rust Rust + Go "echo rust 2\n"|POST  Python "pip install uv\n"` {
		t.Fatalf("writes: %v", writes)
	}
	run("setup-script", "delete", "python", "--workstream", "694")
	if writes[len(writes)-1] != "DELETE ss_py" {
		t.Fatalf("delete: %v", writes)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"unknown name", []string{"setup-script", "get", "Java", "--workstream", "694"}, "", `no setup script named "Java"`},
		{"padded name", []string{"setup-script", "get", " Rust", "--workstream", "694"}, "", "1–60 characters"},
		{"empty script", []string{"setup-script", "put", "Empty", "--workstream", "694"}, "  \n", "non-empty plain text"},
		{"too long", []string{"setup-script", "put", "Long", "--workstream", "694"}, strings.Repeat("x", setupScriptLimit+1), "exceeds 10000"},
		{"server refuses", []string{"setup-script", "put", "Bad", "--workstream", "694"}, "echo", "Setup script rejected: Write a script"},
		{"file on get", []string{"setup-script", "get", "Rust + Go", "--workstream", "694", "--file", file}, "", "Usage: aircom setup-scripts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr.Reset()
			client.Stdin = strings.NewReader(tc.stdin)
			if code := client.Run(tc.args); code == 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
		})
	}
}

func TestMachineStartSendsSetupScriptName(t *testing.T) {
	cred := testCredential()
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"runId":"run_1","state":"booting"}`)
	}))
	defer server.Close()
	client, _, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, cred)
	args := []string{"machine", "start", "--workstream", "694", "--agent", cred.AgentID, "--profile", "Standard", "--repo", "Org/repo", "--setup-script", "Rust + Go"}
	if code := client.Run(args); code != 0 || sent["setupScript"] != "Rust + Go" {
		t.Fatalf("exit %d, sent %v, stderr %s", code, sent, stderr.String())
	}
	if code := client.Run([]string{"machine", "done", "--workstream", "694", "--agent", cred.AgentID, "--run", "run_1", "--setup-script", "Rust"}); code == 0 {
		t.Fatal("setup script accepted on done")
	}
}
