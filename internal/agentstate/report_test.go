package agentstate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReporterK1Wire(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "PUT" || r.URL.EscapedPath() != "/agent/v1/workstreams/abc%2Fdef/agents/me/state" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request: %s %s %s", r.Method, r.URL.EscapedPath(), r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body["source"] != "daemon" || body["at"] != now.Format(time.RFC3339Nano) || body["since"] != now.Format(time.RFC3339Nano) {
			t.Errorf("body: %+v", body)
		}
		if requests == 1 && (body["physical"] != "running" || body["logical"] != "waiting" || body["reason"] != "#7 in flight") {
			t.Errorf("running body: %+v", body)
		}
		if requests == 2 && (body["physical"] != "stopped" || body["logical"] != nil || body["reason"] != "pi closed") {
			t.Errorf("stopped body: %+v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	reporter := Reporter{BaseURL: server.URL, Client: server.Client(), Token: "secret"}
	if err := reporter.Report(context.Background(), "abc/def", State{Physical: Running, Logical: Waiting, Reason: "#7 in flight", Since: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(context.Background(), "abc/def", State{Physical: Stopped, Reason: "pi closed", Since: now}, now); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestReporterValidationAndServerFailure(t *testing.T) {
	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer server.Close()
	reporter := Reporter{BaseURL: server.URL, Client: server.Client(), Token: "secret"}
	for _, bad := range []State{{Physical: Stopped, Logical: Idle, Since: now}, {Physical: Running, Since: now}, {Physical: Stopped, Reason: strings.Repeat("x", 121), Since: now}} {
		if err := reporter.Report(context.Background(), "348", bad, now); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := reporter.Report(context.Background(), "348", State{Physical: Stopped, Since: now}, now); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("failure: %v", err)
	}
}
