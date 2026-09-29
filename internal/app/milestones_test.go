package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkOrderWarnsWhenUnfinishedDependencyFollowsTask(t *testing.T) {
	const detail = `{"workstream":{"code":"694"},"tasks":[{"id":"aaaaaaaaaaaaaaa1","number":1,"title":"First","status":"todo","position":10,"milestone":"A","dependsOn":["aaaaaaaaaaaaaaa2"]},{"id":"aaaaaaaaaaaaaaa2","number":2,"title":"Dependency","status":"todo","position":20,"milestone":"A"}],"updates":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/milestones") {
			_, _ = w.Write([]byte(`[{"name":"A","position":10}]`))
		} else {
			_, _ = w.Write([]byte(detail))
		}
	}))
	defer server.Close()
	client, out, errOut := testApp(t, server.URL, "", deterministicRandom(0x5a))
	saveTestCredential(t, client, testCredential())
	if client.Run([]string{"tasks", "--workstream", "694", "--order", "work"}) != 0 {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(out.String(), "First · waits on #2") {
		t.Fatalf("missing dependency warning: %s", out.String())
	}
}

func TestMilestoneCommandsAndTaskPosition(t *testing.T) {
	var paths []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		if r.Method == http.MethodGet {
			if strings.HasSuffix(r.URL.Path, "/milestones") {
				_, _ = w.Write([]byte(`[{"name":"Phase 2","position":10,"count":2},{"name":"Phase 1","position":20,"count":1}]`))
			} else {
				_, _ = w.Write([]byte(structuredDetail))
			}
			return
		}
		_, _ = w.Write([]byte(`{"name":"Phase 2","position":10}`))
	}))
	defer server.Close()
	client, out, errOut := testApp(t, server.URL, "", deterministicRandom(0x5a))
	saveTestCredential(t, client, testCredential())
	for _, args := range [][]string{{"milestones", "--workstream", "694"}, {"milestone", "Phase 1", "--workstream", "694", "--before", "Phase 2", "--target", "2026-12-01"}, {"task", "5", "--workstream", "694", "--after", "#6"}, {"tasks", "--workstream", "694", "--order", "work"}} {
		if n := client.Run(args); n != 0 {
			t.Fatalf("%v failed: %s", args, errOut.String())
		}
	}
	if !strings.Contains(out.String(), "Phase 2") || !strings.Contains(out.String(), "Task") {
		t.Fatalf("output: %s", out.String())
	}
	want := "PATCH /agent/v1/workstreams/694/tasks/aaaaaaaaaaaaaaa5/position"
	found := false
	for _, p := range paths {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing %s: %v", want, paths)
	}
	hasAfter := false
	for _, b := range bodies {
		if strings.Contains(b, `"after":"#6"`) {
			hasAfter = true
		}
	}
	if !hasAfter {
		t.Fatalf("task position body: %v", bodies)
	}
}
