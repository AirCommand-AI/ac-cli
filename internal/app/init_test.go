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

type fakeDaemonStarter struct{ starts *int }

func (f fakeDaemonStarter) RunDaemon(args []string) error {
	if len(args) != 1 || args[0] != "start" {
		panic("unexpected daemon operation")
	}
	if f.starts != nil {
		*f.starts++
	}
	return nil
}

func TestInitRegistersThenReusesValidDevice(t *testing.T) {
	var redeemed redeemDeviceCodeRequest
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/v1/agents" {
			w.Write([]byte(`{"agents":[]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/ajax/device/redeem" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &redeemed)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"sk-ac-abcdefghijklmnopqrstuvwxyz012345","deviceId":"dev_0123456789abcdef01234567","deviceName":"laptop"}`))
	}))
	defer server.Close()
	stdout := &bytes.Buffer{}
	starts := 0
	client := &App{BaseURL: server.URL, Store: credentials.NewStore(t.TempDir()), Stdin: strings.NewReader("482 913\n"), Stdout: stdout, Stderr: &bytes.Buffer{}, DaemonCommands: fakeDaemonStarter{&starts}, OpenBrowser: func(string) error { t.Fatal("browser opened"); return nil }}
	if err := client.initMachine(nil); err != nil {
		t.Fatal(err)
	}
	if redeemed.Code != "482 913" || redeemed.MachineName == "" || redeemed.Platform == "" {
		t.Fatalf("redeemed %+v", redeemed)
	}
	machine, err := client.Store.LoadMachine()
	if err != nil || machine.MachineName != "laptop" {
		t.Fatalf("machine %+v %v", machine, err)
	}
	stdout.Reset()
	if err := client.initMachine(nil); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "Machine registered: laptop\nDaemon running\n" {
		t.Fatalf("idempotent output %q", got)
	}
	if starts != 2 || len(calls) != 2 || calls[0] != "POST /ajax/device/redeem" || calls[1] != "GET /v1/agents" {
		t.Fatalf("calls %v starts %d", calls, starts)
	}
}

func TestInitRenewsRevokedDevice(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"token":"new-token","deviceId":"new-device","deviceName":"new-name"}`))
	}))
	defer server.Close()
	store := credentials.NewStore(t.TempDir())
	if err := store.SaveMachine(credentials.Machine{APIToken: "old-token", DeviceID: "old-device"}); err != nil {
		t.Fatal(err)
	}
	client := &App{BaseURL: server.URL, Store: store, Stdin: strings.NewReader("ABCD-1234\n"), Stdout: &bytes.Buffer{}, DaemonCommands: fakeDaemonStarter{}}
	if err := client.initMachine(nil); err != nil {
		t.Fatal(err)
	}
	machine, _ := store.LoadMachine()
	if gets != 1 || machine.APIToken != "new-token" {
		t.Fatalf("machine %+v gets %d", machine, gets)
	}
}

func TestInitRejectsBadCodeAndRequiresCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()
	store := credentials.NewStore(t.TempDir())
	client := &App{BaseURL: server.URL, Store: store, Stdin: strings.NewReader("bad\n"), Stdout: &bytes.Buffer{}, DaemonCommands: fakeDaemonStarter{}}
	if err := client.initMachine(nil); err == nil {
		t.Fatal("accepted rejected code")
	}
	if _, err := store.LoadMachine(); err == nil {
		t.Fatal("stored rejected credential")
	}
	client.Stdin = strings.NewReader("\n")
	if err := client.initMachine(nil); err == nil {
		t.Fatal("accepted empty code")
	}
}
