package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// leaveServer is a machine's view of the service for leave, disconnect,
// rejoin and listing. Leaving clears the agent's workstream on the server,
// as the real endpoint does, and repeating it still succeeds.
type leaveServer struct {
	mu          sync.Mutex
	workstreams map[string]string // agent ID -> workstream code
	names       map[string]string
	leaveStatus int
	left        []string
	feedQueries []string
}

func newLeaveServer() *leaveServer {
	return &leaveServer{
		workstreams: map[string]string{leadID: "583", engineerID: "583"},
		names:       map[string]string{leadID: "Lead", engineerID: "Engineer"},
		leaveStatus: http.StatusNoContent,
	}
}

func (s *leaveServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		path := request.URL.Path
		switch {
		case request.Method == http.MethodGet && path == "/v1/organizations":
			_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"},{"organizationId":"org_bbbbbbbbbbbbbbbbbbbbbbbbbb","name":"Beta"}]}`))
		case request.Method == http.MethodGet && path == "/v1/workstreams":
			_, _ = writer.Write([]byte(`{"workstreams":[{"code":"583","name":"Budget"},{"code":"610","name":"Fixes"}]}`))
		case request.Method == http.MethodGet && path == "/v1/agents":
			var agents []agentSummary
			for id, name := range s.names {
				agents = append(agents, agentSummary{AgentID: id, Name: name, Status: "connected", WorkstreamCode: s.workstreams[id]})
			}
			body, _ := json.Marshal(listAgentsResponse{Agents: agents})
			_, _ = writer.Write(body)
		case request.Method == http.MethodDelete && strings.HasSuffix(path, "/workstream"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/agents/"), "/workstream")
			s.left = append(s.left, id)
			if s.leaveStatus < 300 {
				s.workstreams[id] = ""
			}
			writer.WriteHeader(s.leaveStatus)
		case request.Method == http.MethodDelete && strings.HasPrefix(path, "/v1/agents/"):
			id := strings.TrimPrefix(path, "/v1/agents/")
			delete(s.workstreams, id)
			delete(s.names, id)
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/notifications"):
			s.feedQueries = append(s.feedQueries, path+"?"+request.URL.RawQuery)
			_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"fresh","pollAfterSeconds":30}`))
		case request.Method == http.MethodPost && strings.HasPrefix(path, "/v1/agents/"):
			parts := strings.Split(strings.TrimPrefix(path, "/v1/agents/"), "/")
			id, code := parts[0], parts[2]
			s.workstreams[id] = code
			writer.WriteHeader(http.StatusCreated)
			body, _ := json.Marshal(joinResponse{AgentID: id, AgentName: s.names[id], WorkstreamCode: code, SocketAddress: "ac:" + id})
			_, _ = writer.Write(body)
		default:
			t.Errorf("unexpected %s %s", request.Method, path)
		}
	})
}

// leaveFixture is a machine with Lead and Engineer both stored in 583.
func leaveFixture(t *testing.T) (*leaveServer, *App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	fake := newLeaveServer()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	// Each join draws three credentials' worth of randomness; allow two joins.
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33, 0x44, 0x55, 0x66))
	storedAgent(t, client, leadID, "583", "Lead")
	storedAgent(t, client, engineerID, "583", "Engineer")
	return fake, client, stdout, stderr
}

func run(t *testing.T, client *App, stdout, stderr *bytes.Buffer, arguments ...string) (int, string, string) {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	code := client.Run(arguments)
	return code, stdout.String(), stderr.String()
}

func credentialExists(t *testing.T, client *App, agentID string) bool {
	t.Helper()
	_, err := os.Stat(client.Store.Path(agentID))
	return err == nil
}

func TestLeaveForgetsOnlyThatAgentsCredential(t *testing.T) {
	_, client, stdout, stderr := leaveFixture(t)

	if code, _, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("leave exit code = %d, stderr = %q", code, errText)
	}
	if credentialExists(t, client, leadID) {
		t.Fatal("Lead's credential for the workstream it left is still stored")
	}
	if !credentialExists(t, client, engineerID) {
		t.Fatal("leaving as Lead removed Engineer's credential")
	}

	cases := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"machine view", []string{"workstreams", "--org", "Acme"},
			[]string{"* 583      Budget  (on this machine: Engineer)\n"}, []string{"Lead"}},
		{"as the agent that left", []string{"workstreams", "--org", "Acme", "--agent", "Lead"},
			[]string{"  583      Budget  (on this machine: Engineer)\n", "Lead is not in any of these workstreams"}, []string{"you are Lead"}},
		{"as the agent that stayed", []string{"workstreams", "--org", "Acme", "--agent", "Engineer"},
			[]string{"* 583      Budget  (you are Engineer here)\n"}, []string{"also on this machine"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errText := run(t, client, stdout, stderr, tc.args...)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, errText)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("contains %q:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestFailedLeaveKeepsTheCredential(t *testing.T) {
	fake, client, stdout, stderr := leaveFixture(t)
	fake.leaveStatus = http.StatusInternalServerError

	if code, _, _ := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code == 0 {
		t.Fatal("leave reported success when the server refused")
	}
	if !credentialExists(t, client, leadID) {
		t.Fatal("a failed leave discarded a credential that is still valid")
	}
	_, out, _ := run(t, client, stdout, stderr, "workstreams", "--org", "Acme")
	if !strings.Contains(out, "(on this machine: Engineer, Lead)") {
		t.Fatalf("listing lost the agent after a failed leave:\n%s", out)
	}
}

func TestLeaveReportsACredentialItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove files from a read-only directory")
	}
	fake, client, stdout, stderr := leaveFixture(t)
	directory := storagepath.AgentDirectory(client.Store.Home(), leadID)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	code, out, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead")
	if code == 0 {
		t.Fatal("leave reported plain success though the stale credential stayed")
	}
	if len(fake.left) != 1 {
		t.Fatalf("server leave calls = %d, want 1", len(fake.left))
	}
	for _, want := range []string{"Lead has left its workstream", client.Store.Path(leadID), "aircom leave --agent Lead again"} {
		if !strings.Contains(errText, want) {
			t.Errorf("recovery message missing %q: %q", want, errText)
		}
	}
	if strings.Contains(out+errText, "api_") || strings.Contains(out+errText, "sock_") {
		t.Fatalf("output leaked a secret: %q %q", out, errText)
	}

	// Once the file can be removed, running leave again finishes the job:
	// the server treats a repeat leave as success.
	_ = os.Chmod(directory, 0o700)
	if code, _, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("repeat leave exit code = %d, stderr = %q", code, errText)
	}
	if credentialExists(t, client, leadID) {
		t.Fatal("repeat leave did not clear the stale credential")
	}
}

func TestLeaveKeepsTheAgentsOtherFiles(t *testing.T) {
	_, client, stdout, stderr := leaveFixture(t)
	lock, err := agentlock.Acquire(client.Store.Home(), leadID)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Release()

	if code, _, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("leave exit code = %d, stderr = %q", code, errText)
	}
	if _, err := os.Stat(agentlock.Path(client.Store.Home(), leadID)); err != nil {
		t.Fatalf("leave removed the agent's lock file: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(client.Store.Path(leadID))); err != nil {
		t.Fatalf("leave removed the agent's directory: %v", err)
	}
}

func TestRejoinAfterLeaveStoresTheNewWorkstream(t *testing.T) {
	_, client, stdout, stderr := leaveFixture(t)

	if code, _, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("leave exit code = %d, stderr = %q", code, errText)
	}
	if code, _, errText := run(t, client, stdout, stderr, "join", "--agent", "Lead", "--org", "Acme", "--workstream", "610"); code != 0 {
		t.Fatalf("rejoin exit code = %d, stderr = %q", code, errText)
	}
	_, out, _ := run(t, client, stdout, stderr, "workstreams", "--org", "Acme", "--agent", "Lead")
	for _, want := range []string{"* 610      Fixes  (you are Lead here)\n", "  583      Budget  (on this machine: Engineer)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q after rejoin:\n%s", want, out)
		}
	}
}

func TestDisconnectForgetsTheCredential(t *testing.T) {
	_, client, stdout, stderr := leaveFixture(t)

	if code, _, errText := run(t, client, stdout, stderr, "disconnect", "--agent", "Lead"); code != 0 {
		t.Fatalf("disconnect exit code = %d, stderr = %q", code, errText)
	}
	if credentialExists(t, client, leadID) {
		t.Fatal("a disconnected agent's credential is still stored")
	}
	if !credentialExists(t, client, engineerID) {
		t.Fatal("disconnecting Lead removed Engineer's credential")
	}
}

// TestListenAfterMovingStartsFreshInTheNewWorkstream: the cursor from the
// workstream the agent left must not be sent as "since" to the one it joined,
// while a restart in the same workstream still resumes from its cursor.
func TestListenAfterMovingStartsFreshInTheNewWorkstream(t *testing.T) {
	fake, client, stdout, stderr := leaveFixture(t)
	client.ListenPollLimit = 1
	client.ListenSleep = func(time.Duration) {}
	stored, err := client.Store.FindByAgent("583", leadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ListenStore.SaveCursor(leadID, stored.WorkstreamKey(), "old-583-cursor"); err != nil {
		t.Fatal(err)
	}

	// Same workstream restart resumes.
	if code, _, errText := run(t, client, stdout, stderr, "listen", "--workstream", "583", "--agent", leadID); code != 0 {
		t.Fatalf("listen in 583 exit code = %d, stderr = %q", code, errText)
	}
	if got := fake.feedQueries[len(fake.feedQueries)-1]; got != "/agent/v1/workstreams/583/notifications?since=old-583-cursor" {
		t.Fatalf("restart in 583 polled %q; want it to resume from its cursor", got)
	}

	if code, _, errText := run(t, client, stdout, stderr, "leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("leave exit code = %d, stderr = %q", code, errText)
	}
	if code, _, errText := run(t, client, stdout, stderr, "join", "--agent", "Lead", "--org", "Acme", "--workstream", "610"); code != 0 {
		t.Fatalf("join exit code = %d, stderr = %q", code, errText)
	}
	if code, _, errText := run(t, client, stdout, stderr, "listen", "--workstream", "610", "--agent", leadID); code != 0 {
		t.Fatalf("listen in 610 exit code = %d, stderr = %q", code, errText)
	}
	if got := fake.feedQueries[len(fake.feedQueries)-1]; got != "/agent/v1/workstreams/610/notifications?" {
		t.Fatalf("first poll in 610 was %q; want a fresh baseline with no since", got)
	}
	// The fresh baseline is now stored for Acme's 610.
	joined, err := client.Store.FindByAgent("610", leadID)
	if err != nil {
		t.Fatal(err)
	}
	cursor, found, err := client.ListenStore.LoadCursor(leadID, joined.WorkstreamKey())
	if err != nil || !found || cursor != "fresh" {
		t.Fatalf("610 cursor = %q, %v, %v; want the fresh baseline", cursor, found, err)
	}
}

// TestListenAfterMovingToTheSameCodeInAnotherOrganizationStartsFresh: codes
// repeat across organizations, so 610 in Acme and 610 in Beta are different
// workstreams and must not share a cursor.
func TestListenAfterMovingToTheSameCodeInAnotherOrganizationStartsFresh(t *testing.T) {
	fake, client, stdout, stderr := leaveFixture(t)
	client.ListenPollLimit = 1
	client.ListenSleep = func(time.Duration) {}

	steps := [][]string{
		{"leave", "--agent", "Lead"},
		{"join", "--agent", "Lead", "--org", "Acme", "--workstream", "610"},
		{"listen", "--workstream", "610", "--agent", leadID}, // baseline in Acme/610
		{"listen", "--workstream", "610", "--agent", leadID}, // restart resumes
	}
	for _, step := range steps {
		if code, _, errText := run(t, client, stdout, stderr, step...); code != 0 {
			t.Fatalf("%v exit code = %d, stderr = %q", step, code, errText)
		}
	}
	if got := fake.feedQueries[len(fake.feedQueries)-1]; got != "/agent/v1/workstreams/610/notifications?since=fresh" {
		t.Fatalf("restart in Acme/610 polled %q; want it to resume", got)
	}
	acme, err := client.Store.FindByAgent("610", leadID)
	if err != nil {
		t.Fatal(err)
	}
	if acme.OrganizationID != "org_aaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("join stored organization %q; want Acme's", acme.OrganizationID)
	}

	for _, step := range [][]string{
		{"leave", "--agent", "Lead"},
		{"join", "--agent", "Lead", "--org", "Beta", "--workstream", "610"},
		{"listen", "--workstream", "610", "--agent", leadID},
	} {
		if code, _, errText := run(t, client, stdout, stderr, step...); code != 0 {
			t.Fatalf("%v exit code = %d, stderr = %q", step, code, errText)
		}
	}
	if got := fake.feedQueries[len(fake.feedQueries)-1]; got != "/agent/v1/workstreams/610/notifications?" {
		t.Fatalf("first poll in Beta/610 was %q; want a fresh baseline, not Acme's cursor", got)
	}
}
