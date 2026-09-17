package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
