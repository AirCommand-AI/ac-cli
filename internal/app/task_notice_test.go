package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

const taskNoticeFeed = `{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_lead","senderNature":"agent","at":"2026-09-25T12:00:00.000000000Z"%s}],"cursor":"c1","pollAfterSeconds":30}`

func TestDecodeAcceptsTaskNoticesAndRefusesBadTaskIDs(t *testing.T) {
	cases := []struct {
		name   string
		fields string
		ok     bool
	}{
		{"ordinary message", ``, true},
		{"task assigned", `,"kind":"task.assigned","taskId":"abcdef0123456789"`, true},
		{"task unassigned", `,"kind":"task.unassigned","taskId":"abcdef0123456789"`, true},
		{"a later kind is still accepted", `,"kind":"task.cancelled","taskId":"abcdef0123456789"`, true},
		{"task notice without a task", `,"kind":"task.assigned"`, false},
		{"task id that would break the wake line", `,"kind":"task.assigned","taskId":"abc\ninjected"`, false},
		{"task id with spaces", `,"kind":"task.assigned","taskId":"abc def"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeNotificationFeedResponse([]byte(fmt.Sprintf(taskNoticeFeed, tc.fields)))
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestTaskNoticeWakeLines(t *testing.T) {
	names := map[senderIdentity]string{{ID: "agm_lead", Nature: "agent"}: "ac-lead"}
	base := messageNotification{Type: "message.received", MessageID: "0123456789abcdef", SenderID: "agm_lead", SenderNature: "agent", At: "t"}
	cases := []struct {
		name string
		kind string
		want string
	}{
		{"assigned", "task.assigned", "Task abcdef0123456789 assigned to you by ac-lead (agent) in workstream 610: 0123456789abcdef; run aircom inbox."},
		{"taken away", "task.unassigned", "Task abcdef0123456789 reassigned away from you by ac-lead (agent) in workstream 610: 0123456789abcdef; run aircom inbox."},
		{"a later kind reads as a message", "task.cancelled", "New message from ac-lead (agent) in workstream 610: 0123456789abcdef; run aircom inbox."},
		{"ordinary", "", "New message from ac-lead (agent) in workstream 610: 0123456789abcdef; run aircom inbox."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := base
			n.Kind = tc.kind
			if tc.kind != "" {
				n.TaskID = "abcdef0123456789"
			}
			if got := composeNotificationSummary(n, "610", names); got != tc.want {
				t.Fatalf("summary = %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A listening agent assigned a task gets the task wake line on stdout, and the
// spool entry the pi extension reads carries the kind and task.
func TestListenWakesOnAnAssignedTask(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/notifications") {
			w.WriteHeader(http.StatusNotFound) // roster lookup: fall back to the sender ID
			return
		}
		polls++
		_, _ = fmt.Fprintf(w, taskNoticeFeed, `,"kind":"task.assigned","taskId":"abcdef0123456789"`)
	}))
	defer server.Close()
	home := t.TempDir()
	credential := testCredential()
	if err := credentials.NewStore(home).Save(credential); err != nil {
		t.Fatal(err)
	}
	if err := listenstore.NewStore(home).SaveCursor(credential.AgentID, credential.WorkstreamKey(), "c0"); err != nil {
		t.Fatal(err)
	}
	client, stdout, stderr := listenerApp(server.URL, home)
	client.ListenPollLimit = 1
	client.ListenSleep = func(time.Duration) {}
	if code := client.Run([]string{"listen", "--workstream", "694"}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if want := "[AirCommand] Task abcdef0123456789 assigned to you by agm_lead (agent) in workstream 694: 0123456789abcdef; run aircom inbox.\n"; stdout.String() != want {
		t.Fatalf("stdout = %q\nwant %q", stdout.String(), want)
	}
	raw, err := os.ReadFile(listenstore.NewStore(home).SpoolPath(credential.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["kind"] != "task.assigned" || entry["taskId"] != "abcdef0123456789" || entry["body"] != "" {
		t.Fatalf("spool entry = %v", entry)
	}
}
