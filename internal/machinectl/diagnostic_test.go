package machinectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type noIdleSource struct{ fakeStatusSource }

func (*noIdleSource) IdleSince(context.Context) (*time.Time, error) { return nil, nil }
func TestMachineStatusFailureMatchesDashboardFixtureAndClears(t *testing.T) {
	source := &noIdleSource{}
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		_, _ = w.Write([]byte(`{"machine":{"state":"online"}}`))
	}))
	defer server.Close()
	reporter := &HTTPReporter{URL: server.URL, Token: "device", Version: "v0.22.0", Source: source, RunMode: true}
	reporter.SetCheckFailure(errors.New(`reconcile: agm_1: parsing time ""`), time.Date(2026, 10, 7, 20, 30, 0, 0, time.UTC))
	if err := reporter.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/check-in-error.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !bytes.Equal(bodies[0], bytes.TrimSpace(fixture)) {
		t.Fatalf("wire check-in drifted: %s vs %s", bodies[0], fixture)
	}
	reporter.SetCheckFailure(nil, time.Time{})
	if err := reporter.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	var recovered struct {
		LastError   string `json:"lastError"`
		LastErrorAt string `json:"lastErrorAt"`
	}
	if len(bodies) != 2 || json.Unmarshal(bodies[1], &recovered) != nil || recovered.LastError != "" || recovered.LastErrorAt != "" {
		t.Fatalf("successful check did not clear: %s", bodies[1])
	}
}
func TestMachineErrorSanitizesSecretsAndBoundsRunes(t *testing.T) {
	err := errors.New("git https://alice:password@github.com/org/repo?token=abcdef Bearer sk-ac-abc ghp_abcdef access=abcdef \"my-refresh-token\" " + strings.Repeat("é", 350))
	out := safeMachineError(err)
	for _, secret := range []string{"alice", "password", "github.com", "sk-ac-abc", "ghp_abcdef", "abcdef", "my-refresh-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("leaked %q in %q", secret, out)
		}
	}
	if len([]rune(out)) > 300 {
		t.Fatalf("diagnostic too long: %d", len([]rune(out)))
	}
	if got := safeMachineError(errors.New(`parsing time ""`)); got != `parsing time ""` {
		t.Fatalf("actionable error lost: %q", got)
	}
}
