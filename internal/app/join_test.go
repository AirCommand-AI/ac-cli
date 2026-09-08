package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

// storedAgent puts an already-joined agent on a test machine.
func storedAgent(t *testing.T, client *App, agentID, workstreamCode, agentName string) {
	t.Helper()
	if err := client.Store.SaveMachine(credentials.Machine{
		APIToken:       "sk-ac-abcdefghijklmnopqrstuvwxyz012345",
		OrganizationID: "org_test",
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

func TestJoinReturnsTheExistingAgentInsteadOfCreatingADuplicate(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Pi")

	// A restarted runtime asking to join again means "give me my agent back".
	if exitCode := client.Run([]string{"join", "--workstream", "694", "--name", "Pi"}); exitCode != 0 {
		t.Fatalf("join exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if calls != 0 {
		t.Fatalf("join called the server %d times, want 0 when reusing", calls)
	}
	output := stdout.String()
	if !strings.HasPrefix(output, "Agent ID: agm_existing\n") {
		t.Fatalf("reuse output does not identify the existing agent: %q", output)
	}
	for _, want := range []string{"--agent agm_existing", "Agent name: Pi", "Workstream: 694", "ac:agm_existing"} {
		if !strings.Contains(output, want) {
			t.Errorf("reuse output %q is missing %q", output, want)
		}
	}
}

func TestJoinMatchesAnExistingAgentWithoutRegardToCase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("join called the server instead of reusing the stored agent")
	}))
	defer server.Close()

	client, stdout, _ := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Pi")

	// Addressing a message by name ignores case, so reuse must too.
	if exitCode := client.Run([]string{"join", "--workstream", "694", "--name", "pi"}); exitCode != 0 {
		t.Fatal("join did not reuse an agent whose name differed only in case")
	}
	if !strings.Contains(stdout.String(), "agm_existing") {
		t.Fatalf("reuse output = %q", stdout.String())
	}
}

func TestJoinRefusesToShareAnAgentAnotherLiveSessionHolds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("join called the server while a live session held the agent")
	}))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Pi")

	// Two sessions on one agent share a poll cursor and silently split
	// notifications between them, so a held agent must never be handed out.
	lock, err := agentlock.Acquire(client.Store.Home(), "agm_existing")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if exitCode := client.Run([]string{"join", "--workstream", "694", "--name", "Pi"}); exitCode == 0 {
		t.Fatal("join succeeded while another live session held the agent")
	}
	message := stderr.String()
	if !strings.Contains(message, "already running an agent called Pi") || !strings.Contains(message, "different name") {
		t.Fatalf("held-agent message does not say what to do next: %q", message)
	}
}

func TestJoinDoesNotReuseAnAgentFromAnotherWorkstreamOrName(t *testing.T) {
	client, _, _ := testApp(t, "http://127.0.0.1:1", "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Pi")

	// Neither a different workstream nor a different name is this agent, so
	// both must fall through to a real join rather than silently reusing it.
	for _, pair := range [][2]string{{"165", "Pi"}, {"694", "Claude"}} {
		resumed, err := client.agentToResume(pair[0], pair[1])
		if err != nil {
			t.Fatalf("agentToResume(%q, %q): %v", pair[0], pair[1], err)
		}
		if resumed != nil {
			t.Fatalf("workstream %q name %q reused unrelated agent %q", pair[0], pair[1], resumed.AgentID)
		}
	}
}

func TestJoinWithoutANameResumesTheOnlyAgentThisMachineHasThere(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("join called the server instead of resuming the stored agent")
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Reviewer")

	// "Rejoin 694" carries no name, and the runtime cannot be expected to
	// remember a name its operator chose in an earlier session.
	if exitCode := client.Run([]string{"join", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("join exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Agent name: Reviewer") {
		t.Fatalf("nameless join did not resume the stored agent: %q", stdout.String())
	}
}

func TestJoinWithoutANameAsksWhichAgentWhenSeveralCouldMatch(t *testing.T) {
	client, _, stderr := testApp(t, "http://127.0.0.1:1", "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_one", "694", "Claude")
	storedAgent(t, client, "agm_two", "694", "Pi")

	// Picking one would strand the other: still active, still addressable,
	// with nobody listening as it.
	if exitCode := client.Run([]string{"join", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("nameless join silently picked one of several agents")
	}
	message := stderr.String()
	for _, want := range []string{"more than one agent", "Claude", "Pi", "--name"} {
		if !strings.Contains(message, want) {
			t.Fatalf("ambiguous join message %q is missing %q", message, want)
		}
	}
}

func TestJoinWithoutANameRequiresOneWhenNothingCanBeResumed(t *testing.T) {
	client, _, stderr := testApp(t, "http://127.0.0.1:1", "", deterministicRandom(0x11, 0x22, 0x33))
	if err := client.Store.SaveMachine(credentials.Machine{
		APIToken:       "sk-ac-abcdefghijklmnopqrstuvwxyz012345",
		OrganizationID: "org_test",
	}); err != nil {
		t.Fatalf("SaveMachine: %v", err)
	}

	if exitCode := client.Run([]string{"join", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("join created an agent without being given a name")
	}
	if !strings.Contains(stderr.String(), "Pass --name") {
		t.Fatalf("first-join message does not ask for a name: %q", stderr.String())
	}
}

func TestJoinWithoutANameRefusesWhenEveryLocalAgentIsInUse(t *testing.T) {
	client, _, stderr := testApp(t, "http://127.0.0.1:1", "", deterministicRandom(0x11, 0x22, 0x33))
	storedAgent(t, client, "agm_existing", "694", "Pi")

	lock, err := agentlock.Acquire(client.Store.Home(), "agm_existing")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if exitCode := client.Run([]string{"join", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("nameless join handed over an agent a live session was using")
	}
	message := stderr.String()
	if !strings.Contains(message, "in use by a live session") || !strings.Contains(message, "Pass --name") {
		t.Fatalf("in-use message does not say what to do next: %q", message)
	}
}
