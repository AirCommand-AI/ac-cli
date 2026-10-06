package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/machinectl"
	"github.com/AirCommand-AI/ac-cli/internal/runmode"
)

type reconcileCount struct{ calls int }

func (c *reconcileCount) Reconcile(context.Context) error { c.calls++; return nil }

var _ machinectl.Reconciler = (*reconcileCount)(nil)

func TestRunReconcilerSkipsFinishingAndBooting(t *testing.T) {
	state := "booting"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"runId":"run_test","state":"` + state + `"}`))
	}))
	defer server.Close()
	store := credentials.NewStore(t.TempDir())
	_ = store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "token"})
	counter := &reconcileCount{}
	gate := runningOnlyReconciler{Reconciler: counter, API: runmode.API{BaseURL: server.URL, Client: server.Client(), Store: store}}
	for _, phase := range []string{"booting", "running", "finishing"} {
		state = phase
		if err := gate.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if counter.calls != 1 {
		t.Fatalf("reconciled %d times, want only running", counter.calls)
	}
}
