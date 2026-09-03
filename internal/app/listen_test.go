package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestListenEstablishesSilentBaselineThenComposesAndSpoolsOneWake(t *testing.T) {
	t.Parallel()

	const (
		baselineCursor = "2026-09-01T19:05:34.138976142Z#b3c2e435a1b2c3d4"
		messageCursor  = "2026-09-01T19:06:34.138976142Z#c4d3f546b2c3d4e5"
		messageID      = "c4d3f546b2c3d4e5"
		summary        = "New message from TestBar (agent) in workstream 694: c4d3f546b2c3d4e5; run ac-cli inbox."
	)

	credential := testCredential()
	notificationRequests := 0
	rosterRequests := 0
	messagePosted := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}

		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/agent/v1/workstreams/694":
			rosterRequests++
			_, _ = writer.Write([]byte(`{"code":"694","collaborators":[{"accountId":"ac_operator","name":"Operator","agents":[{"agentId":"agm_sender","name":" TestBar ","status":"active"}]}]}`))
		case "/agent/v1/workstreams/694/notifications":
			notificationRequests++
			since, hasSince := request.URL.Query()["since"]
			switch {
			case !hasSince:
				_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"message.received","messageId":"a2b1d32490abcdef","senderId":"agm_history","senderNature":"agent","at":"2026-08-18T19:04:34.138976142Z"},{"type":"message.received","messageId":"b3c2e435a1b2c3d4","senderId":"agm_history","senderNature":"agent","at":"2026-09-01T19:05:34.138976142Z"}],"cursor":%q,"pollAfterSeconds":30}`, baselineCursor)
			case len(since) == 1 && since[0] == baselineCursor:
				if !messagePosted {
					t.Error("listener requested post-baseline messages before the test posted one")
				}
				if !strings.Contains(request.RequestURI, "%23") || request.URL.Fragment != "" {
					t.Errorf("cursor was not safely URL-encoded: %q", request.RequestURI)
				}
				_, _ = fmt.Fprintf(writer, `{"notifications":[{"type":"message.received","messageId":%q,"senderId":"agm_sender","senderNature":"agent","at":"2026-09-01T19:06:34.138976142Z"}],"cursor":%q,"pollAfterSeconds":30}`, messageID, messageCursor)
			case len(since) == 1 && since[0] == messageCursor:
				_, _ = fmt.Fprintf(writer, `{"notifications":[],"cursor":%q,"pollAfterSeconds":30}`, messageCursor)
			default:
				t.Errorf("unexpected since cursor %q", request.URL.Query().Get("since"))
				writer.WriteHeader(http.StatusBadRequest)
			}
		default:
			t.Errorf("unexpected listener path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
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
		if rosterRequests != 0 {
			t.Fatalf("silent baseline made %d roster requests, want 0", rosterRequests)
		}
		messagePosted = true
	}
	if exitCode := client.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode != 0 {
		t.Fatalf("initial listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if got, want := stdout.String(), "[AirCommand] "+summary+"\n"; got != want {
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
	if notificationRequests != 3 || rosterRequests != 1 {
		t.Fatalf("notification requests = %d, roster requests = %d; want 3, 1", notificationRequests, rosterRequests)
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
	assertFileMode(t, statePath, 0o600)

	spoolPath := stateStore.SpoolPath(credential.AgentID)
	spool, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	spoolLines := strings.Split(strings.TrimSuffix(string(spool), "\n"), "\n")
	if len(spoolLines) != 1 {
		t.Fatalf("spool line count = %d, want 1", len(spoolLines))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(spoolLines[0]), &fields); err != nil {
		t.Fatalf("decode spool fields: %v", err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if want := []string{"at", "messageId", "senderId", "senderNature", "summary", "type"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("spool fields = %v, want %v", keys, want)
	}
	var spooled spooledMessageNotification
	if err := json.Unmarshal([]byte(spoolLines[0]), &spooled); err != nil {
		t.Fatalf("decode spool notification: %v", err)
	}
	if spooled != (spooledMessageNotification{
		Type:         "message.received",
		MessageID:    messageID,
		SenderID:     "agm_sender",
		SenderNature: "agent",
		At:           "2026-09-01T19:06:34.138976142Z",
		Summary:      summary,
	}) {
		t.Fatalf("spooled notification did not preserve the pointer and composed summary: %+v", spooled)
	}
	assertFileMode(t, spoolPath, 0o600)
}

func TestListenCachesRosterNamesAndFallsBackToUnknownSenderID(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	home := t.TempDir()
	if err := credentials.NewStore(home).Save(credential); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	stateStore := listenstore.NewStore(home)
	if err := stateStore.SaveCursor(credential.AgentID, "2026-09-04T12:00:00.000000000Z#0000000000000000"); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	feedRequests := 0
	rosterRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/agent/v1/workstreams/694":
			rosterRequests++
			_, _ = writer.Write([]byte(`{"code":"694","collaborators":[{"accountId":"ac_alice","name":" Alice ","agents":[{"agentId":"agm_pi","name":" Pi ","status":"stopped"}]}]}`))
		case "/agent/v1/workstreams/694/notifications":
			feedRequests++
			notifications := []string{
				`{"type":"message.received","messageId":"1111111111111111","senderId":"agm_pi","senderNature":"agent","at":"2026-09-04T12:01:00.000000000Z"}`,
				`{"type":"message.received","messageId":"2222222222222222","senderId":"ac_alice","senderNature":"human","at":"2026-09-04T12:02:00.000000000Z"}`,
				`{"type":"message.received","messageId":"3333333333333333","senderId":"agm_unknown","senderNature":"agent","at":"2026-09-04T12:03:00.000000000Z"}`,
			}
			cursors := []string{
				"2026-09-04T12:01:00.000000000Z#1111111111111111",
				"2026-09-04T12:02:00.000000000Z#2222222222222222",
				"2026-09-04T12:03:00.000000000Z#3333333333333333",
			}
			_, _ = fmt.Fprintf(writer, `{"notifications":[%s],"cursor":%q,"pollAfterSeconds":30}`, notifications[feedRequests-1], cursors[feedRequests-1])
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 3
	client.ListenSleep = func(time.Duration) {}
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if feedRequests != 3 || rosterRequests != 1 {
		t.Fatalf("feed requests = %d, roster requests = %d; want 3, 1", feedRequests, rosterRequests)
	}
	wantLines := []string{
		"[AirCommand] New message from Pi (agent) in workstream 694: 1111111111111111; run ac-cli inbox.",
		"[AirCommand] New message from Alice (human) in workstream 694: 2222222222222222; run ac-cli inbox.",
		"[AirCommand] New message from agm_unknown (agent) in workstream 694: 3333333333333333; run ac-cli inbox.",
	}
	if got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n"); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("wake lines = %v, want %v", got, wantLines)
	}
	spool, err := os.ReadFile(stateStore.SpoolPath(credential.AgentID))
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if got := strings.Count(string(spool), "\n"); got != 3 {
		t.Fatalf("spool line count = %d, want 3", got)
	}
	for _, expected := range []string{"Pi (agent)", "Alice (human)", "agm_unknown (agent)"} {
		if !strings.Contains(string(spool), expected) {
			t.Errorf("spool does not contain composed sender %q", expected)
		}
	}
}

func TestListenEmptyBaselinePersistsCursorAndContinuesWithPresentEmptySince(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		since, hasSince := request.URL.Query()["since"]
		if requests == 1 && hasSince {
			t.Error("initial baseline included a since parameter")
		}
		if requests == 2 && (!hasSince || len(since) != 1 || since[0] != "" || !strings.Contains(request.RequestURI, "since=")) {
			t.Errorf("continuation after empty baseline did not send ?since=: %q", request.RequestURI)
		}
		_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"","pollAfterSeconds":30}`))
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 2
	client.ListenSleep = func(time.Duration) {}
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("empty polls produced output %q", stdout.String())
	}
	cursor, found, err := listenstore.NewStore(home).LoadCursor(testCredential().AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if !found || cursor != "" {
		t.Fatalf("empty baseline cursor = %q, found = %v; want empty, true", cursor, found)
	}
	if _, err := os.Stat(listenstore.NewStore(home).SpoolPath(testCredential().AgentID)); !os.IsNotExist(err) {
		t.Fatalf("empty baseline created a spool: %v", err)
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

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/agent/v1/workstreams/694/notifications" {
					t.Errorf("listener path = %q, want notification feed", request.URL.Path)
				}
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

func TestListenRetriesVisibleHTTPFailuresWithSameCursorAndBackoff(t *testing.T) {
	t.Parallel()

	const cursor = "2026-09-04T12:00:00.000000000Z#0123456789abcdef"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.URL.Query().Get("since"); got != cursor {
			t.Errorf("retry cursor = %q, want %q", got, cursor)
		}
		switch requests {
		case 1:
			writer.WriteHeader(http.StatusRequestTimeout)
			_, _ = writer.Write([]byte(`{"message":"Request timeout","code":"RequestTimeout"}`))
		case 2:
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"message":"Internal Server Error","code":"InternalServerError"}`))
		case 3:
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"message":"Notification feed could not be completed; retry the request","code":"NotificationFeedUnavailable"}`))
		case 4:
			_, _ = fmt.Fprintf(writer, `{"notifications":[],"cursor":%q,"pollAfterSeconds":30}`, cursor)
		default:
			t.Errorf("unexpected request %d", requests)
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	stateStore := listenstore.NewStore(home)
	if err := stateStore.SaveCursor(testCredential().AgentID, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 4
	var sleeps []time.Duration
	client.ListenSleep = func(delay time.Duration) { sleeps = append(sleeps, delay) }
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := strings.Join([]string{
		"[AirCommand] Lost connection: AirCommand notification request failed (HTTP 408)",
		"[AirCommand] Lost connection: AirCommand notification request failed (HTTP 500)",
		"[AirCommand] Lost connection: AirCommand notification feed unavailable (HTTP 503)",
		"[AirCommand] Connection restored.",
		"",
	}, "\n")
	if got := stdout.String(); got != want {
		t.Fatalf("retry output = %q, want %q", got, want)
	}
	if wantSleeps := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}; !reflect.DeepEqual(sleeps, wantSleeps) {
		t.Fatalf("retry sleeps = %v, want %v", sleeps, wantSleeps)
	}
	persisted, found, err := stateStore.LoadCursor(testCredential().AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if !found || persisted != cursor {
		t.Fatalf("failed requests changed cursor to %q, found = %v", persisted, found)
	}
}

func TestListenInvalidCursorStopsWithoutChangingStateOrLeakingResponse(t *testing.T) {
	t.Parallel()

	const cursor = "2026-09-04T12:00:00.000000000Z#0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"message":"Invalid notification cursor","code":"InvalidNotificationCursor","body":"must_not_be_printed"}`))
	}))
	defer server.Close()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	stateStore := listenstore.NewStore(home)
	if err := stateStore.SaveCursor(testCredential().AgentID, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("invalid cursor unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "stored notification cursor is invalid") {
		t.Fatalf("invalid-cursor stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "must_not_be_printed") {
		t.Fatal("notification error response reached output")
	}
	persisted, found, err := stateStore.LoadCursor(testCredential().AgentID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if !found || persisted != cursor {
		t.Fatalf("invalid cursor response changed state to %q, found = %v", persisted, found)
	}
}

func TestListenDoesNotRetryKnownFinalStatusWhenResponseBodyReadFails(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := credentials.NewStore(home).Save(testCredential()); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	client, stdout, stderr := listenerApp("https://unused.invalid", home)
	transport := &statusReadFailureTransport{status: http.StatusBadRequest}
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"listen", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("final listener status unexpectedly succeeded")
	}
	if transport.calls != 1 {
		t.Fatalf("final listener status produced %d requests, want 1", transport.calls)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 400") {
		t.Fatalf("final listener status stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestListenPrintsNetworkFailureAndRecovery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/agent/v1/workstreams/694/notifications" {
			t.Errorf("listener path = %q, want notification feed", request.URL.Path)
		}
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

func TestDecodeNotificationFeedRequiresPointerOnlyContractFields(t *testing.T) {
	t.Parallel()

	valid := `{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_sender","senderNature":"agent","at":"2026-09-04T12:34:56.123456789Z"}],"cursor":"cursor","pollAfterSeconds":30}`
	if _, err := decodeNotificationFeedResponse([]byte(valid)); err != nil {
		t.Fatalf("valid notification response rejected: %v", err)
	}
	invalid := []string{
		`{"cursor":"","pollAfterSeconds":30}`,
		`{"notifications":null,"cursor":"","pollAfterSeconds":30}`,
		`{"notifications":[],"pollAfterSeconds":30}`,
		`{"notifications":[],"cursor":""}`,
		`{"notifications":[{"type":"workstream.message","messageId":"0123456789abcdef","senderId":"agm_sender","senderNature":"agent","at":"2026-09-04T12:34:56.123456789Z"}],"cursor":"cursor","pollAfterSeconds":30}`,
		`{"notifications":[{"type":"message.received","messageId":"short","senderId":"agm_sender","senderNature":"agent","at":"2026-09-04T12:34:56.123456789Z"}],"cursor":"cursor","pollAfterSeconds":30}`,
		`{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"","senderNature":"agent","at":"2026-09-04T12:34:56.123456789Z"}],"cursor":"cursor","pollAfterSeconds":30}`,
		`{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_sender","senderNature":"system","at":"2026-09-04T12:34:56.123456789Z"}],"cursor":"cursor","pollAfterSeconds":30}`,
		`{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_sender","senderNature":"agent","at":""}],"cursor":"cursor","pollAfterSeconds":30}`,
	}
	for index, body := range invalid {
		if _, err := decodeNotificationFeedResponse([]byte(body)); err == nil {
			t.Errorf("invalid notification response %d was accepted", index)
		}
	}
}

func TestNotificationFailureReasonMaps503CodesWithoutEchoingBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		body string
		want string
	}{
		{body: `{"error":"Service Unavailable","code":"ServiceUnavailable","body":"secret"}`, want: "authentication service unavailable"},
		{body: `{"message":"Notification feed could not be completed; retry the request","code":"NotificationFeedUnavailable","body":"secret"}`, want: "notification feed unavailable"},
	}
	for _, test := range tests {
		reason := notificationFailureReason(http.StatusServiceUnavailable, []byte(test.body))
		if !strings.Contains(reason, test.want) || strings.Contains(reason, "secret") {
			t.Errorf("failure reason = %q, want safe text containing %q", reason, test.want)
		}
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

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("file %q mode = %o, want %o", path, got, want)
	}
}
