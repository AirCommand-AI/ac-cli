package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActivityPullsOneFilteredPageWithOpaqueCursor(t *testing.T) {
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested = true
		if request.Method != http.MethodGet || request.URL.Path != "/agent/v1/workstreams/694/events" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("kind") != "task" || query.Get("task") != "task-17" || query.Get("limit") != "25" || query.Get("cursor") != "opaque+/=" {
			t.Fatalf("query = %v", query)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"events":[{"eventId":"evt-1","kind":"task.created"}],"nextCursor":"older","latestCursor":"latest"}`))
	}))
	defer server.Close()
	app, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(1))
	if err := app.Store.Save(testCredential()); err != nil {
		t.Fatal(err)
	}
	if code := app.Run([]string{"activity", "--workstream", "694", "--kind", "task", "--task", "task-17", "--limit", "25", "--cursor", "opaque+/="}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !requested || !strings.Contains(stdout.String(), `"eventId":"evt-1"`) || !strings.Contains(stdout.String(), `"nextCursor":"older"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestActivityRejectsTwoCursorDirectionsBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	app, _, stderr := testApp(t, server.URL, "", deterministicRandom(1))
	if err := app.Store.Save(testCredential()); err != nil {
		t.Fatal(err)
	}
	if code := app.Run([]string{"activity", "--workstream", "694", "--cursor", "older", "--after", "newer"}); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if requests != 0 || !strings.Contains(stderr.String(), "not both") {
		t.Fatalf("requests = %d, stderr = %q", requests, stderr.String())
	}
}

func TestActivityHelpCallsOutPullPaging(t *testing.T) {
	app, stdout, stderr := testApp(t, "http://example.invalid", "", deterministicRandom(1))
	if code := app.Run([]string{"activity", "--help"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--cursor C") || !strings.Contains(stdout.String(), "--after C") {
		t.Fatalf("help = %q", stdout.String())
	}
}
