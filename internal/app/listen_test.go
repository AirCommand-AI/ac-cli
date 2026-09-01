package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestListenPersistsCursorAcrossRestartsWithoutDuplicateOutput(t *testing.T) {
	t.Parallel()

	const (
		cursorOne   = "2026-09-01T19:05:34.138976142Z#b3c2e435"
		cursorTwo   = "2026-09-01T19:06:34.138976142Z#c4d3f546"
		summaryOne  = "New message on workstream 694 from TestFoo"
		summaryTwo  = "New task message on workstream 694 from TestBar"
		messageBody = "body must never reach output"
	)

	credential := testCredential()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if request.URL.Path != "/agent/v1/workstreams/694/messages" {
			t.Errorf("path = %q, want message endpoint", request.URL.Path)
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if _, present := request.URL.Query()["since"]; !present {
			t.Error("since query parameter is missing")
		}

		writer.Header().Set("Content-Type", "application/json")
		switch since := request.URL.Query().Get("since"); since {
		case "":
			_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"workstream.message","updateId":"b3c2e435","author":"TestFoo","taskId":"","at":"2026-09-01T19:05:34.138976142Z","summary":%q,"body":%q}],"cursor":%q,"pollAfterSeconds":30}`, summaryOne, messageBody, cursorOne)
		case cursorOne:
			_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"task.message","updateId":"c4d3f546","author":"TestBar","taskId":"task-1","at":"2026-09-01T19:06:34.138976142Z","summary":%q}],"cursor":%q,"pollAfterSeconds":30}`, summaryTwo, cursorTwo)
		case cursorTwo:
			_, _ = fmt.Fprintf(writer, `{"notifications":[],"cursor":%q,"pollAfterSeconds":30}`, cursorTwo)
		default:
			t.Errorf("unexpected since cursor %q", since)
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.Save(credential); err != nil {
		t.Fatalf("Save credential: %v", err)
	}

	var outputs []string
	for restart := 0; restart < 3; restart++ {
		client, stdout, stderr := listenerApp(server.URL, home)
		client.ListenPollLimit = 1
		if exitCode := client.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode != 0 {
			t.Fatalf("restart %d exit code = %d, stderr = %q", restart, exitCode, stderr.String())
		}
		outputs = append(outputs, stdout.String())
	}
	if requests != 3 {
		t.Fatalf("message requests = %d, want 3", requests)
	}
	if got, want := outputs[0], "[AirCommand] "+summaryOne+"\n"; got != want {
		t.Fatalf("first output = %q, want %q", got, want)
	}
	if got, want := outputs[1], "[AirCommand] "+summaryTwo+"\n"; got != want {
		t.Fatalf("second output = %q, want %q", got, want)
	}
	if outputs[2] != "" {
		t.Fatalf("already-seen notifications produced output %q", outputs[2])
	}
	combinedOutput := strings.Join(outputs, "")
	if strings.Count(combinedOutput, summaryOne) != 1 || strings.Count(combinedOutput, summaryTwo) != 1 {
		t.Fatalf("notification output was duplicated: %q", combinedOutput)
	}
	if strings.Contains(combinedOutput, messageBody) {
		t.Fatal("message body reached stdout")
	}
	if strings.Contains(combinedOutput, cursorOne) || strings.Contains(combinedOutput, cursorTwo) {
		t.Fatal("cursor reached stdout")
	}
	if strings.Contains(combinedOutput, credential.APIToken) || strings.Contains(combinedOutput, credential.SocketKey) {
		t.Fatal("credential reached stdout")
	}

	stateStore := listenstore.NewStore(home)
	cursor, err := stateStore.LoadCursor("694", credential.AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if cursor != cursorTwo {
		t.Fatalf("persisted cursor = %q, want %q", cursor, cursorTwo)
	}
	statePath := stateStore.StatePath("694", credential.AgentID)
	if want := filepath.Join(home, ".aircommand", "state", "694-agent-7.json"); statePath != want {
		t.Fatalf("state path = %q, want %q", statePath, want)
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat cursor state: %v", err)
	}
	if got := stateInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("cursor state mode = %o, want 600", got)
	}

	spoolPath := stateStore.SpoolPath("694")
	spool, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if strings.Contains(string(spool), messageBody) {
		t.Fatal("message body reached spool")
	}
	spoolLines := strings.Split(strings.TrimSuffix(string(spool), "\n"), "\n")
	if len(spoolLines) != 2 {
		t.Fatalf("spool line count = %d, want 2", len(spoolLines))
	}
	for index, line := range spoolLines {
		var notification messageNotification
		if err := json.Unmarshal([]byte(line), &notification); err != nil {
			t.Fatalf("decode spool line %d: %v", index+1, err)
		}
		wantSummary := []string{summaryOne, summaryTwo}[index]
		if notification.Summary != wantSummary {
			t.Fatalf("spool summary %d = %q, want %q", index+1, notification.Summary, wantSummary)
		}
		if !strings.Contains(outputs[index], notification.Summary) {
			t.Fatalf("spool line %d and stdout are out of sync", index+1)
		}
	}
	spoolInfo, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat spool: %v", err)
	}
	if got := spoolInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("spool mode = %o, want 600", got)
	}
}

func TestListenEmptyPollPrintsNothing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"","pollAfterSeconds":30}`))
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 1
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("empty poll output = %q, want empty", stdout.String())
	}
}

func TestListenEnforcesPollFloor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"","pollAfterSeconds":1}`))
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 2
	var sleeps []time.Duration
	client.ListenSleep = func(delay time.Duration) { sleeps = append(sleeps, delay) }
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("empty polls produced output %q", stdout.String())
	}
	if len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("poll sleeps = %v, want [5s]", sleeps)
	}
	if got := pollDelay(nil); got != 30*time.Second {
		t.Fatalf("default poll delay = %v, want 30s", got)
	}
}

func TestListenTerminalFailureLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: "[AirCommand] You were stopped or removed from workstream 694.\n"},
		{name: "not found", status: http.StatusNotFound, want: "[AirCommand] Workstream 694 no longer exists.\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"credential":"must not be printed"}`))
			}))
			defer server.Close()

			home := t.TempDir()
			if err := credentials.NewStore(home).Save(testCredential()); err != nil {
				t.Fatalf("Save credential: %v", err)
			}
			client, stdout, stderr := listenerApp(server.URL, home)
			if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode == 0 {
				t.Fatal("terminal listener failure exited successfully")
			}
			if got := stdout.String(); got != test.want {
				t.Fatalf("stdout = %q, want %q", got, test.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("terminal listener diagnostic = %q, want empty", stderr.String())
			}
		})
	}
}

func TestListenPrintsNetworkFailureAndRecovery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"","pollAfterSeconds":30}`))
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 2
	client.HTTPClient = &http.Client{Transport: &failOnceTransport{base: http.DefaultTransport}}
	var sleeps []time.Duration
	client.ListenSleep = func(delay time.Duration) { sleeps = append(sleeps, delay) }
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "[AirCommand] Lost connection: simulated transport failure\n[AirCommand] Connection restored.\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("network backoff = %v, want [5s]", sleeps)
	}
}

func listenerApp(baseURL string, home string) (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	return &App{
		BaseURL:     baseURL,
		HTTPClient:  http.DefaultClient,
		Store:       credentials.NewStore(home),
		ListenStore: listenstore.NewStore(home),
		Stdout:      stdout,
		Stderr:      stderr,
	}, stdout, stderr
}
