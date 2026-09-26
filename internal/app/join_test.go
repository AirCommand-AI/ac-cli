package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

// storedAgent puts an already-joined agent on a test machine.
func storedAgent(t *testing.T, client *App, agentID, workstreamCode, agentName string) {
	t.Helper()
	if err := client.Store.SaveMachine(credentials.Machine{
		APIToken: "sk-ac-abcdefghijklmnopqrstuvwxyz012345",
		DeviceID: "dev_0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("SaveMachine: %v", err)
	}
	if err := client.Store.Save(credentials.Credential{
		APIToken:       "api_1111111111111111111111111111111111111111111111111111111111111111",
		SocketKey:      "sock_2222222222222222222222222222222222222222222222222222222222222222",
		WorkstreamCode: workstreamCode,
		AgentID:        agentID,
		SocketAddress:  "ac:" + agentID,
		AgentName:      agentName,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// storedMachine registers this machine locally, which every join needs before
// it can ask the service anything.
func storedMachine(t *testing.T, client *App) {
	t.Helper()
	if err := client.Store.SaveMachine(credentials.Machine{
		APIToken: "sk-ac-abcdefghijklmnopqrstuvwxyz012345",
		DeviceID: "dev_0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("SaveMachine: %v", err)
	}
}

// joinTestServer serves the three reads join now makes: which organizations
// this machine can reach, which agents are on it, and the notification feed.
func joinTestServer(t *testing.T, agent agentSummary, onNotify func()) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/v1/organizations":
			_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case request.URL.Path == "/v1/agents":
			body, _ := json.Marshal(listAgentsResponse{Agents: []agentSummary{agent}})
			_, _ = writer.Write(body)
		case strings.HasPrefix(request.URL.Path, "/agent/v1/workstreams/") && !strings.Contains(request.URL.Path, "/notifications"):
			_, _ = writer.Write([]byte(`{"workstream":{"code":"694"}}`))
		case strings.Contains(request.URL.Path, "/notifications"):
			if onNotify != nil {
				onNotify()
			}
			_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"c1","pollAfterSeconds":5}`))
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
}

// TestJoinOnAnAgentAlreadyThereReportsItsIdentity covers a restarted runtime
// asking to join where it already is: that is not an error, it is how it gets
// its agent back.
func TestJoinOnAnAgentAlreadyThereReportsItsIdentity(t *testing.T) {
	agent := agentSummary{
		AgentID: "agm_0123456789abcdef0123456789abcdef", Name: "Pi",
		Status: "connected", OrganizationID: "org_aaaaaaaaaaaaaaaaaaaaaaaaaa", WorkstreamCode: "694",
	}
	server := joinTestServer(t, agent, nil)
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, agent.AgentID, "694", "Pi")

	if exitCode := client.Run([]string{"join", "--agent", "Pi", "--org", "Acme", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("join exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), agent.AgentID) {
		t.Fatalf("identity not reported on stdout: %q", stdout.String())
	}
}

// TestJoinWithListenStreamsWakeLinesOnStdout keeps the identity block off the
// wake-line stream a harness turns into notifications.
func TestJoinWithListenStreamsWakeLinesOnStdout(t *testing.T) {
	notified := 0
	agent := agentSummary{
		AgentID: "agm_0123456789abcdef0123456789abcdef", Name: "Pi",
		Status: "connected", OrganizationID: "org_aaaaaaaaaaaaaaaaaaaaaaaaaa", WorkstreamCode: "694",
	}
	server := joinTestServer(t, agent, func() { notified++ })
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, agent.AgentID, "694", "Pi")
	client.ListenPollLimit = 2
	client.ListenSleep = func(time.Duration) {}

	if exitCode := client.Run([]string{"join", "--agent", "Pi", "--org", "Acme", "--workstream", "694", "--listen"}); exitCode != 0 {
		t.Fatalf("join --listen exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if notified == 0 {
		t.Fatal("join --listen returned without ever polling for notifications")
	}
	if strings.Contains(stdout.String(), "Agent ID:") {
		t.Fatalf("identity block reached the wake-line stream: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Agent ID: "+agent.AgentID) {
		t.Fatalf("identity block missing from stderr: %q", stderr.String())
	}
}

// TestJoinRefusesAnAgentAlreadySomewhereElse makes moving deliberate: an agent
// is in one workstream at a time, so join says to leave first rather than
// silently moving it.
func TestJoinRefusesAnAgentAlreadySomewhereElse(t *testing.T) {
	agent := agentSummary{
		AgentID: "agm_0123456789abcdef0123456789abcdef", Name: "Pi",
		Status: "connected", OrganizationID: "org_aaaaaaaaaaaaaaaaaaaaaaaaaa", WorkstreamCode: "720",
	}
	server := joinTestServer(t, agent, nil)
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedMachine(t, client)
	if exitCode := client.Run([]string{"join", "--agent", "Pi", "--org", "Acme", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("join moved an agent that was already in a workstream")
	}
	if !strings.Contains(stderr.String(), "aircom leave") {
		t.Fatalf("error does not say how to move it: %q", stderr.String())
	}
}

// TestJoinResolvesAnUnknownOrganizationHelpfully lists what is reachable rather
// than just refusing.
func TestJoinResolvesAnUnknownOrganizationHelpfully(t *testing.T) {
	agent := agentSummary{AgentID: "agm_0123456789abcdef0123456789abcdef", Name: "Pi", Status: "connected"}
	server := joinTestServer(t, agent, nil)
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedMachine(t, client)
	if exitCode := client.Run([]string{"join", "--agent", "Pi", "--org", "Nowhere", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("an unknown organization was accepted")
	}
	if !strings.Contains(stderr.String(), "Acme") {
		t.Fatalf("error does not name what is reachable: %q", stderr.String())
	}
}

// TestRemovedAgentsAreHiddenAndDoNotCollide keeps a disconnected agent from
// appearing in the list or making its old name ambiguous.
func TestRemovedAgentsAreHiddenAndDoNotCollide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"agents":[
			{"agentId":"agm_11111111111111111111111111111111","name":"Probe","status":"retired"},
			{"agentId":"agm_22222222222222222222222222222222","name":"Probe","status":"connected"}]}`))
	}))
	defer server.Close()

	client, stdout, _ := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedMachine(t, client)

	agent, err := client.resolveAgent("Probe")
	if err != nil {
		t.Fatalf("resolveAgent: %v", err)
	}
	if agent.AgentID != "agm_22222222222222222222222222222222" {
		t.Fatalf("resolved %q; want the live agent", agent.AgentID)
	}
	if exitCode := client.Run([]string{"agents"}); exitCode != 0 {
		t.Fatal("agents failed")
	}
	if strings.Contains(stdout.String(), "agm_11111111111111111111111111111111") {
		t.Fatalf("a removed agent was listed: %q", stdout.String())
	}
}

// assignmentServer serves an agent whose assignment appears after a number of
// polls, and records the join it then receives.
type assignmentServer struct {
	pollsBeforeAssigned int
	polls               int
	joinedPath          string
	joinedOrganization  string
}

func (s *assignmentServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/agents":
			agent := agentSummary{AgentID: "agm_0123456789abcdef0123456789abcdef", Name: "Pi", Status: "connected"}
			if s.polls >= s.pollsBeforeAssigned {
				agent.AssignedOrganizationID = "org_aaaaaaaaaaaaaaaaaaaaaaaaaa"
				agent.AssignedWorkstreamCode = "694"
			}
			s.polls++
			body, _ := json.Marshal(listAgentsResponse{Agents: []agentSummary{agent}})
			_, _ = writer.Write(body)
		case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/v1/agents/"):
			s.joinedPath = request.URL.Path
			s.joinedOrganization = request.Header.Get(organizationHeader)
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"agentId":"agm_0123456789abcdef0123456789abcdef","agentName":"Pi","workstreamCode":"694","socketAddress":"ac:agm_0123456789abcdef0123456789abcdef","generation":1}`))
		default:
			t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		}
	})
}

func TestJoinPicksUpAnAssignmentFromTheDashboard(t *testing.T) {
	fake := &assignmentServer{pollsBeforeAssigned: 0}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedMachine(t, client)
	if exitCode := client.Run([]string{"join", "--agent", "Pi"}); exitCode != 0 {
		t.Fatalf("join exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if fake.joinedPath != "/v1/agents/agm_0123456789abcdef0123456789abcdef/workstreams/694" {
		t.Fatalf("joined %q; want the assigned workstream", fake.joinedPath)
	}
	// The organization comes from the assignment, sent as the header the
	// server verifies.
	if fake.joinedOrganization != "org_aaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("organization header = %q", fake.joinedOrganization)
	}
}

// TestJoinWaitsForAnAssignmentUnderListen is what lets a click in the
// dashboard take effect without anyone typing a command.
func TestJoinWaitsForAnAssignmentUnderListen(t *testing.T) {
	fake := &assignmentServer{pollsBeforeAssigned: 3}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedMachine(t, client)
	var slept int
	client.ListenSleep = func(time.Duration) { slept++ }
	client.ListenPollLimit = 10

	// Only the wait is under test here, so stop before listening starts.
	agent, err := client.resolveAgent("Pi")
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.awaitAssignment(agent, true)
	if err != nil {
		t.Fatalf("awaitAssignment: %v", err)
	}
	if got.AssignedWorkstreamCode != "694" || slept == 0 {
		t.Fatalf("got %+v after %d waits; want the assignment after waiting", got, slept)
	}
	if !strings.Contains(stderr.String(), "Waiting for Pi") {
		t.Fatalf("waiting was not reported on stderr: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Waiting") {
		t.Fatalf("waiting message reached the wake-line stream: %q", stdout.String())
	}
}

// TestJoinWithListenRefusesAnAgentRunningElsewhere keeps a second session from
// becoming the same agent: it must not wait, and must not join, while another
// process on this machine holds the agent.
func TestJoinWithListenRefusesAnAgentRunningElsewhere(t *testing.T) {
	fake := &assignmentServer{pollsBeforeAssigned: 0}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedMachine(t, client)
	other, err := agentlock.Acquire(client.Store.Home(), "agm_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = other.Release() }()

	if exitCode := client.Run([]string{"join", "--agent", "Pi", "--listen"}); exitCode == 0 {
		t.Fatal("a second session joined as an agent already running")
	}
	if !strings.Contains(stderr.String(), "already running in another session") {
		t.Fatalf("refusal does not say why: %q", stderr.String())
	}
	if fake.joinedPath != "" {
		t.Fatal("joined while another session held the agent")
	}
}

func TestJoinWithNothingAssignedAndNoListenSaysHow(t *testing.T) {
	fake := &assignmentServer{pollsBeforeAssigned: 100}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedMachine(t, client)
	if exitCode := client.Run([]string{"join", "--agent", "Pi"}); exitCode == 0 {
		t.Fatal("join with nothing to join succeeded")
	}
	if !strings.Contains(stderr.String(), "account page") {
		t.Fatalf("error does not say how to proceed: %q", stderr.String())
	}
	if fake.joinedPath != "" {
		t.Fatal("joined without an assignment")
	}
}

func TestJoinNamingOnlyOneOfOrgAndWorkstreamIsRefused(t *testing.T) {
	client, _, _ := testApp(t, "http://127.0.0.1:1", "", deterministicRandom(0x11))
	for _, arguments := range [][]string{
		{"join", "--agent", "Pi", "--org", "Acme"},
		{"join", "--agent", "Pi", "--workstream", "694"},
	} {
		if exitCode := client.Run(arguments); exitCode == 0 {
			t.Fatalf("%v succeeded; want a usage error", arguments)
		}
	}
}
