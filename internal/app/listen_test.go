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

func TestListenEstablishesSilentBaselineThenWakesOnceWithoutRestartDuplicate(t *testing.T) {
	t.Parallel()

	const (
		baselineCursor = "2026-09-01T19:05:34.138976142Z#b3c2e435"
		messageCursor  = "2026-09-01T19:06:34.138976142Z#c4d3f546"
		historicalOne  = "Historical message on workstream 694 from TestFoo"
		historicalTwo  = "Older historical message on workstream 694 from TestBaz"
		newSummary     = "New task message on workstream 694 from TestBar"
		messageBody    = "body must never reach output"
	)

	credential := testCredential()
	requests := 0
	messagePosted := false
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

		writer.Header().Set("Content-Type", "application/json")
		since, hasSince := request.URL.Query()["since"]
		switch {
		case !hasSince:
			_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"workstream.message","updateId":"a2b1d324","author":"TestBaz","taskId":"","at":"2026-08-18T19:04:34.138976142Z","summary":%q,"body":%q},{"type":"workstream.message","updateId":"b3c2e435","author":"TestFoo","taskId":"","at":"2026-09-01T19:05:34.138976142Z","summary":%q,"body":%q}],"cursor":%q,"pollAfterSeconds":30}`, historicalTwo, messageBody, historicalOne, messageBody, baselineCursor)
		case len(since) == 1 && since[0] == baselineCursor:
			if !messagePosted {
				t.Error("listener requested post-baseline messages before the test posted one")
			}
			_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"task.message","updateId":"c4d3f546","author":"TestBar","taskId":"task-1","at":"2026-09-01T19:06:34.138976142Z","summary":%q,"body":%q}],"cursor":%q,"pollAfterSeconds":30}`, newSummary, messageBody, messageCursor)
		case len(since) == 1 && since[0] == messageCursor:
			_, _ = fmt.Fprintf(writer, `{"notifications":[],"cursor":%q,"pollAfterSeconds":30}`, messageCursor)
		default:
			t.Errorf("unexpected since cursor %q", request.URL.Query().Get("since"))
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(credential); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	stateStore := listenstore.NewStore(home)

	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 2
	client.ListenSleep = func(_ time.Duration) {
		cursor, found, err := stateStore.LoadCursor(credential.AgentID)
		if err != nil {
			t.Fatalf("LoadCursor after baseline: %v", err)
		}
		if !found || cursor != baselineCursor {
			t.Fatalf("baseline cursor = %q, found = %v; want %q, true", cursor, found, baselineCursor)
		}
		if stdout.Len() != 0 {
			t.Fatalf("first poll replayed history: %q", stdout.String())
		}
		if _, err := os.Stat(stateStore.SpoolPath(credential.AgentID)); !os.IsNotExist(err) {
			t.Fatalf("first poll created a notification spool, stat error = %v", err)
		}
		messagePosted = true
	}
	if exitCode := client.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode != 0 {
		t.Fatalf("initial listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if got, want := stdout.String(), "[AirCommand] "+newSummary+"\n"; got != want {
		t.Fatalf("initial listen output = %q, want %q", got, want)
	}

	restarted, restartStdout, restartStderr := listenerApp(server.URL, home)
	restarted.ListenPollLimit = 1
	if exitCode := restarted.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode != 0 {
		t.Fatalf("restart exit code = %d, stderr = %q", exitCode, restartStderr.String())
	}
	if restartStdout.Len() != 0 {
		t.Fatalf("restart duplicated notification output %q", restartStdout.String())
	}
	if requests != 3 {
		t.Fatalf("message requests = %d, want 3", requests)
	}

	combinedOutput := stdout.String() + restartStdout.String()
	for _, forbidden := range []string{historicalOne, historicalTwo, messageBody, baselineCursor, messageCursor, credential.APIToken, credential.SocketKey} {
		if strings.Contains(combinedOutput, forbidden) {
			t.Fatalf("listener output contains protected or historical value %q", forbidden)
		}
	}
	if strings.Count(combinedOutput, newSummary) != 1 {
		t.Fatalf("new notification output count != 1: %q", combinedOutput)
	}

	cursor, found, err := stateStore.LoadCursor(credential.AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if !found || cursor != messageCursor {
		t.Fatalf("persisted cursor = %q, found = %v; want %q, true", cursor, found, messageCursor)
	}
	statePath := stateStore.StatePath(credential.AgentID)
	if want := filepath.Join(home, ".aircommand", "agents", "agent-7", "state.json"); statePath != want {
		t.Fatalf("state path = %q, want %q", statePath, want)
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat cursor state: %v", err)
	}
	if got := stateInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("cursor state mode = %o, want 600", got)
	}

	spoolPath := stateStore.SpoolPath(credential.AgentID)
	spool, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	spoolText := string(spool)
	for _, forbidden := range []string{historicalOne, historicalTwo, messageBody} {
		if strings.Contains(spoolText, forbidden) {
			t.Fatalf("notification spool contains protected or historical value %q", forbidden)
		}
	}
	spoolLines := strings.Split(strings.TrimSuffix(spoolText, "\n"), "\n")
	if len(spoolLines) != 1 {
		t.Fatalf("spool line count = %d, want 1", len(spoolLines))
	}
	var notification messageNotification
	if err := json.Unmarshal([]byte(spoolLines[0]), &notification); err != nil {
		t.Fatalf("decode spool line: %v", err)
	}
	if notification.Summary != newSummary {
		t.Fatalf("spool summary = %q, want %q", notification.Summary, newSummary)
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
	cursor, found, err := listenstore.NewStore(home).LoadCursor(testCredential().AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if !found || cursor != "" {
		t.Fatalf("empty baseline cursor = %q, found = %v; want empty, true", cursor, found)
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
