package agentapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchAndCursor(t *testing.T) {
	const body = `{"notifications":[],"cursor":"c1","pollAfterSeconds":30}`
	for _, tc := range []struct {
		stored       bool
		cursor, want string
	}{{false, "", "/agent/v1/workstreams/626/notifications"}, {true, "", "/agent/v1/workstreams/626/notifications?since="}, {true, "a#b", "/agent/v1/workstreams/626/notifications?since=a%23b"}} {
		feed, status, _, err := Fetch(func(method, path, token string, payload []byte) (int, []byte, error) {
			if method != http.MethodGet || path != tc.want || token != "secret" {
				t.Errorf("request = %q %q %q", method, path, token)
			}
			return 200, []byte(body), nil
		}, "626", "secret", tc.cursor, tc.stored)
		if err != nil || status != 200 || *feed.Cursor != "c1" {
			t.Fatalf("feed = %+v, %d, %v", feed, status, err)
		}
	}
}
func TestWakeAndTerminal(t *testing.T) {
	n := Notification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "agm_lead", SenderNature: "agent", Kind: "task.assigned", TaskID: "abc", Priority: "urgent"}
	names := LoadSenderNames([]Sender{{ID: "agm_lead", Nature: "agent", Name: "Lead\nName"}})
	summary := ComposeSummary(n, "626", names)
	if !strings.HasPrefix(summary, "URGENT Task abc assigned to you by Lead Name") || Spool(n, summary).SenderID != n.SenderID {
		t.Fatal(summary)
	}
	if !TerminalStatus(409, "AgentStopped") || TerminalStatus(409, "Other") || !TerminalStatus(401, "") {
		t.Fatal("terminal classification")
	}
	if PollDelay(nil) != 30*time.Second {
		t.Fatal("poll delay")
	}
}
func TestAmbiguousSendersAndRedaction(t *testing.T) {
	names := LoadSenderNames([]Sender{{"a", "agent", "Alice"}, {"a", "agent", "Bob"}})
	if len(names) != 0 {
		t.Fatal(names)
	}
	if got := Redact("long short", "long", "long short"); got != "[REDACTED]" {
		t.Fatal(got)
	}
}
