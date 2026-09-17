package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

// TestInitRedeemsTheCodeAndStoresADevice covers the browser-to-terminal flow:
// the page shows a code, the human types it here, and the credential arrives as
// the direct answer — no polling.
func TestInitRedeemsTheCodeAndStoresADevice(t *testing.T) {
	var redeemed redeemDeviceCodeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ajax/device/redeem" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &redeemed)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"sk-ac-abcdefghijklmnopqrstuvwxyz012345","deviceId":"dev_0123456789abcdef01234567","deviceName":"laptop"}`))
	}))
	defer server.Close()

	home := t.TempDir()
	var opened string
	stdout := &bytes.Buffer{}
	client := &App{
		BaseURL:     server.URL,
		Store:       credentials.NewStore(home),
		Stdin:       strings.NewReader("482 913\n"),
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
		OpenBrowser: func(url string) error { opened = url; return nil },
	}

	if err := client.initMachine(nil); err != nil {
		t.Fatalf("initMachine: %v", err)
	}

	if !strings.HasSuffix(opened, "/device") {
		t.Fatalf("opened %q; want the device page", opened)
	}
	// The code is sent as typed; normalising formatting is the server's job,
	// so a human pasting "482 913" is not rejected by the client.
	if strings.TrimSpace(redeemed.Code) == "" {
		t.Fatal("no code was sent")
	}
	if redeemed.MachineName == "" || redeemed.Platform == "" {
		t.Fatalf("machine metadata not sent: %+v", redeemed)
	}

	machine, err := client.Store.LoadMachine()
	if err != nil {
		t.Fatalf("LoadMachine: %v", err)
	}
	if machine.DeviceID != "dev_0123456789abcdef01234567" || machine.APIToken == "" {
		t.Fatalf("stored machine = %+v", machine)
	}
}

// TestInitRejectsABadCodeWithoutStoringAnything keeps a failed registration
// from leaving a half-written credential behind.
func TestInitRejectsABadCodeWithoutStoringAnything(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"That code is not valid. Ask for a new one."}`))
	}))
	defer server.Close()

	home := t.TempDir()
	client := &App{
		BaseURL:     server.URL,
		Store:       credentials.NewStore(home),
		Stdin:       strings.NewReader("000000\n"),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		OpenBrowser: func(string) error { return nil },
	}

	err := client.initMachine(nil)
	if err == nil {
		t.Fatal("a rejected code was treated as success")
	}
	if _, loadErr := client.Store.LoadMachine(); loadErr == nil {
		t.Fatal("a rejected registration stored a credential")
	}
}

// TestInitRequiresACode stops an empty line being sent as a guess.
func TestInitRequiresACode(t *testing.T) {
	client := &App{
		BaseURL:     "http://127.0.0.1:1",
		Store:       credentials.NewStore(t.TempDir()),
		Stdin:       strings.NewReader("\n"),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		OpenBrowser: func(string) error { return nil },
	}
	if err := client.initMachine(nil); err == nil {
		t.Fatal("an empty code was accepted")
	}
}
