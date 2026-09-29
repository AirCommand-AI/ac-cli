package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/listenstore"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestSupervisorPollSkipsMalformedNotificationAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	store := credentials.NewStore(m.Home)
	cred := credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, OrganizationID: "org_1", APIToken: "secret", SocketKey: "socket", SocketAddress: "ac:agm_1"}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("since") {
			_, _ = w.Write([]byte(`{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"ac_1","senderNature":"human","at":"now","kind":"approval.decided"},{"type":"message.received","messageId":"fedcba9876543210","senderId":"ac_1","senderNature":"human","at":"now","kind":"task.assigned"},{"type":"message.received","messageId":"abcdef0123456789","senderId":"agm_1","senderNature":"agent","at":"now"}],"cursor":"c2","pollAfterSeconds":5}`))
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[],"cursor":"c1","pollAfterSeconds":5}`))
	}))
	defer server.Close()
	m.Poll = &HTTPPoller{BaseURL: server.URL, Client: server.Client(), Store: store}
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	spool, err := os.ReadFile(listenstore.NewStore(m.Home).SpoolPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(spool), "messageId") != 2 || !strings.Contains(string(spool), "0123456789abcdef") || strings.Contains(string(spool), "fedcba9876543210") {
		t.Fatalf("spool = %s", spool)
	}
	cursor, has, err := listenstore.NewStore(m.Home).LoadCursor(d.AgentID, cred.WorkstreamKey())
	if err != nil || !has || cursor != "c2" {
		t.Fatalf("cursor = %q, %t, %v", cursor, has, err)
	}
}

func TestHTTPPollerSharedAgentAPIAndTerminalClassification(t *testing.T) {
	home := t.TempDir()
	d := definition(home)
	store := credentials.NewStore(home)
	if err := store.Save(credentials.Credential{AgentID: d.AgentID, WorkstreamCode: d.Workstream, OrganizationID: "org_1", APIToken: "secret", SocketKey: "socket", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	var path string
	code := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.String()
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong credential")
		}
		w.WriteHeader(code)
		if code != http.StatusOK {
			_, _ = w.Write([]byte(`{"code":"AgentStopped","message":"secret"}`))
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_sender","senderNature":"agent","at":"now"}],"cursor":"c1","pollAfterSeconds":30}`))
	}))
	defer server.Close()
	p := &HTTPPoller{BaseURL: server.URL, Client: server.Client(), Store: store}
	feed, err := p.Fetch(context.Background(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/agent/v1/workstreams/626/notifications" || len(feed.Notifications) != 1 {
		t.Fatal(path, feed)
	}
	spool, err := p.Spool(context.Background(), d, feed.Notifications[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(spool)
	if !strings.Contains(string(data), "agm_sender") || !strings.Contains(string(data), "run aircom inbox") {
		t.Fatal(string(data))
	}
	_, err = p.Fetch(context.Background(), d, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "since=") {
		t.Fatal("empty saved cursor not sent", path)
	}
	code = http.StatusConflict
	_, err = p.Fetch(context.Background(), d, "c1", true)
	if !errors.Is(err, ErrAgentStopped) {
		t.Fatalf("409 AgentStopped not terminal: %v", err)
	}
}
