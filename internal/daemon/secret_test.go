package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestMachineSecretFetchAndStore(t *testing.T) {
	store := credentials.NewStore(t.TempDir())
	if err := store.SaveMachine(credentials.Machine{APIToken: "machine-token", DeviceID: "device_123"}); err != nil {
		t.Fatal(err)
	}
	secret := "msock_" + strings.Repeat("a", 64)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/machine/socket-secret" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		// The dashboard answers 201 Created (ac-dashboard api/machine_socket.go).
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"machineSocketSecret":"` + secret + `"}`))
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		got, err := MachineSecret(context.Background(), store, server.Client(), server.URL)
		if err != nil || got != secret {
			t.Fatalf("secret %q %v", got, err)
		}
	}
	if requests != 1 {
		t.Fatalf("secret rotated %d times", requests)
	}
	loaded, err := store.LoadMachine()
	if err != nil || loaded.MachineSocketSecret != secret {
		t.Fatalf("stored %+v %v", loaded, err)
	}
}
