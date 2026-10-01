package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestAttachUpdateUsesMachineNameAndOneLineConfirmation(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/workstreams/694/updates" {
			t.Errorf("route %s", r.URL.Path)
		}
		var payload updateRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		body = payload.Body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"update-1","author":"agent-7","summary":"posted","createdAt":"2026-10-01T19:30:00Z"}`))
	}))
	defer server.Close()
	a, out, _ := testApp(t, server.URL, "", nil)
	if err := a.Store.Save(testCredential()); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SaveMachine(credentials.Machine{APIToken: "machine-token", DeviceID: "dev-1", MachineName: "studio-mac"}); err != nil {
		t.Fatal(err)
	}
	if err := a.postAttachUpdate("694", "agent-7", "hello"); err != nil {
		t.Fatal(err)
	}
	if body != "Operator (attach on studio-mac): hello" {
		t.Fatalf("body %q", body)
	}
	if got := out.String(); got != "[AirCommand] Update posted: update-1\n" {
		t.Fatalf("attach confirmation %q", got)
	}
}

func TestAttachUpdateFallsBackToHostname(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload updateRequest
		_ = json.NewDecoder(r.Body).Decode(&payload)
		body = payload.Body
		_, _ = w.Write([]byte(`{"id":"update-1","summary":"posted","createdAt":"2026-10-01T19:30:00Z"}`))
	}))
	defer server.Close()
	a, _, _ := testApp(t, server.URL, "", nil)
	if err := a.Store.Save(testCredential()); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SaveMachine(credentials.Machine{APIToken: "machine-token", DeviceID: "dev-1"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.Stdout = &out
	if err := a.postAttachUpdate("694", "agent-7", "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "attach on "+machineName()+")") || strings.Contains(body, "attach on machine)") {
		t.Fatalf("fallback body %q", body)
	}
}
