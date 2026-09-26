package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

// listenClock is the listener's clock in tests: sleeping advances it, so an
// outage of minutes runs instantly.
type listenClock struct {
	now    time.Time
	sleeps []time.Duration
}

func attachListenClock(client *App) *listenClock {
	clock := &listenClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	client.ListenNow = func() time.Time { return clock.now }
	client.ListenSleep = func(delay time.Duration) {
		clock.sleeps = append(clock.sleeps, delay)
		clock.now = clock.now.Add(delay)
	}
	return clock
}

// scriptedFeed answers the notification feed from a script: "503" and "fail"
// (transport error, via failingTransport) are failures, "empty" is a quiet
// poll, "message" delivers one notification, "401" is terminal.
type scriptedFeed struct {
	mu      sync.Mutex
	script  []string
	polls   int
	cursors []string
}

func (f *scriptedFeed) next() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	step := f.script[f.polls]
	f.polls++
	return step
}

func (f *scriptedFeed) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/notifications") {
			// The roster lookup for sender names is not part of the script; with
			// none available the wake line names the sender by ID.
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.cursors = append(f.cursors, request.URL.Query().Get("since"))
		f.mu.Unlock()
		switch step := f.next(); step {
		case "503":
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"message":"retry","code":"NotificationFeedUnavailable"}`))
		case "401":
			writer.WriteHeader(http.StatusUnauthorized)
		case "empty":
			_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"c0","pollAfterSeconds":30}`))
		case "message":
			_, _ = writer.Write([]byte(`{"notifications":[{"type":"message.received","messageId":"1111111111111111","senderId":"agm_pi","senderNature":"agent","at":"2026-09-25T12:05:00.000000000Z"}],"cursor":"c1","pollAfterSeconds":30}`))
		default:
			t.Errorf("unexpected script step %q", step)
		}
	})
}

// failingTransport fails the requests the script marks "fail" and passes the
// rest to the scripted server.
type failingTransport struct {
	feed *scriptedFeed
	base http.RoundTripper
}

func (tr *failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(request.URL.Path, "/notifications") {
		return tr.base.RoundTrip(request)
	}
	tr.feed.mu.Lock()
	step := tr.feed.script[tr.feed.polls]
	if step == "fail" {
		tr.feed.polls++
		tr.feed.cursors = append(tr.feed.cursors, request.URL.Query().Get("since"))
	}
	tr.feed.mu.Unlock()
	if step == "fail" {
		return nil, errors.New("simulated transport failure")
	}
	return tr.base.RoundTrip(request)
}

// runScript listens once per script step from a stored cursor and returns
// stdout, the listener's sleeps, the cursors it sent, and the exit code.
func runScript(t *testing.T, script ...string) (string, *listenClock, *scriptedFeed, *App, int) {
	t.Helper()
	feed := &scriptedFeed{script: script}
	server := httptest.NewServer(feed.handler(t))
	t.Cleanup(server.Close)
	home := t.TempDir()
	credential := testCredential()
	if err := credentials.NewStore(home).Save(credential); err != nil {
		t.Fatal(err)
	}
	if err := listenstore.NewStore(home).SaveCursor(credential.AgentID, credential.WorkstreamKey(), "c0"); err != nil {
		t.Fatal(err)
	}
	client, stdout, _ := listenerApp(server.URL, home)
	client.HTTPClient = &http.Client{Transport: &failingTransport{feed: feed, base: http.DefaultTransport}}
	client.ListenPollLimit = len(script)
	clock := attachListenClock(client)
	code := client.Run([]string{"listen", "--workstream", "694"})
	return stdout.String(), clock, feed, client, code
}

func TestListenAnnouncesOnlyProlongedOutages(t *testing.T) {
	t.Parallel()

	const lost503 = "[AirCommand] Lost connection: AirCommand notification feed unavailable (HTTP 503)\n"
	const lostNet = "[AirCommand] Lost connection: simulated transport failure\n"
	const restored = "[AirCommand] Connection restored.\n"
	const message = "[AirCommand] New message from agm_pi (agent) in workstream 694: 1111111111111111; run aircom inbox.\n"

	cases := []struct {
		name   string
		script []string
		want   string
	}{
		{"brief HTTP outage is silent",
			[]string{"503", "503", "503", "empty"}, ""},
		{"brief network outage is silent",
			[]string{"fail", "fail", "empty"}, ""},
		// Failures at 0, 5, 15, 35, 65 and 95 seconds: the fifth is the first at
		// or past a minute, and the sixth must not repeat it.
		{"prolonged HTTP outage announces once, then recovery once",
			[]string{"503", "503", "503", "503", "503", "503", "empty"}, lost503 + restored},
		{"prolonged network outage announces once, then recovery once",
			[]string{"fail", "fail", "fail", "fail", "fail", "fail", "empty"}, lostNet + restored},
		{"a later brief outage stays silent",
			[]string{"503", "503", "503", "503", "503", "empty", "503", "empty"}, lost503 + restored},
		{"a message after a brief outage is still delivered",
			[]string{"503", "503", "message"}, message},
		{"a message after a prolonged outage follows the recovery",
			[]string{"fail", "fail", "fail", "fail", "fail", "message"}, lostNet + restored + message},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, _, feed, _, code := runScript(t, tc.script...)
			if code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			if got := stripListenerTimestamps(out); got != tc.want {
				t.Fatalf("stdout = %q, want %q", out, tc.want)
			}
			// Failed polls never move the cursor.
			for i, step := range tc.script {
				if step != "503" && step != "fail" {
					break
				}
				if feed.cursors[i] != "c0" {
					t.Fatalf("poll %d sent since=%q, want the stored cursor", i+1, feed.cursors[i])
				}
			}
		})
	}
}

func TestListenOutageKeepsBackoffCursorAndSpool(t *testing.T) {
	t.Parallel()

	_, clock, feed, client, code := runScript(t, "503", "503", "503", "503", "503", "503", "empty")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	if !reflect.DeepEqual(clock.sleeps, want) {
		t.Fatalf("backoff = %v, want %v", clock.sleeps, want)
	}
	if got := feed.cursors[len(feed.cursors)-1]; got != "c0" {
		t.Fatalf("recovery poll sent since=%q, want the stored cursor", got)
	}
	// Outage lines are for the runtime reading stdout, not messages: nothing
	// is appended to the spool the pi extension watches.
	if _, err := os.Stat(client.ListenStore.SpoolPath(testCredential().AgentID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outage wrote to the spool: %v", err)
	}
}

func TestListenStopsAtOnceOnATerminalFailureDuringAnOutage(t *testing.T) {
	t.Parallel()

	out, _, _, _, code := runScript(t, "503", "503", "401")
	if code == 0 {
		t.Fatal("listener kept running after being removed")
	}
	want := fmt.Sprintf("[AirCommand] You were stopped or removed from workstream %s.\n", testCredential().WorkstreamCode)
	if got := stripListenerTimestamps(out); got != want {
		t.Fatalf("stdout = %q, want only the terminal line %q", out, want)
	}
	if strings.Contains(out, "Lost connection") {
		t.Fatalf("a brief outage before the terminal failure was announced: %q", out)
	}
}
