package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

// Every command that uses a per-agent credential takes --agent as an ID or a
// name. The fake service refuses everything with 401, which each command
// handles as a final answer; what matters is which agent's token it sent.

var agentCommandFamilies = map[string][]string{
	"send":   {"send", "--workstream", "694", "--to", "agm_other", "--body", "hi"},
	"update": {"update", "--workstream", "694", "--body", "hi"},
	"read":   {"read", "--workstream", "694"},
	"task":   {"task", "t1", "--workstream", "694"},
	"tasks":  {"tasks", "--workstream", "694"},
	"inbox":  {"inbox", "--workstream", "694"},
	"ack":    {"ack", "--workstream", "694", "--message", "1111111111111111"},
	"listen": {"listen", "--workstream", "694"},
}

type tokenRecorder struct {
	mu     sync.Mutex
	tokens []string
}

func (r *tokenRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		r.mu.Lock()
		r.tokens = append(r.tokens, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
		r.mu.Unlock()
		writer.WriteHeader(http.StatusUnauthorized)
	}))
}

// namedAgentsMachine stores Pi and Claude in 694, "Twin" twice in 694, and
// Scout in 610.
func namedAgentsMachine(t *testing.T, client *App) map[string]string {
	t.Helper()
	tokens := map[string]string{}
	for _, agent := range []struct{ id, code, name string }{
		{"agm_pi", "694", "Pi"},
		{"agm_claude", "694", "Claude"},
		{"agm_twin1", "694", "Twin"},
		{"agm_twin2", "694", "Twin"},
		{"agm_scout", "610", "Scout"},
		// Agents in 694 named like IDs: one after Scout's real ID, one after an
		// ID that exists nowhere. Neither may be selected by that ID-shaped name.
		{"agm_namedscout", "694", "agm_scout"},
		{"agm_namedghost", "694", "agm_ghost"},
	} {
		token := "api_" + strings.Repeat(agent.id[4:5], 64)
		token = token[:len("api_")+64]
		if agent.id == "agm_namedghost" {
			token = "api_" + strings.Repeat("g", 64)
		}
		if err := client.Store.Save(credentials.Credential{
			APIToken: token, SocketKey: "sock_" + strings.Repeat("0", 64), WorkstreamCode: agent.code,
			AgentID: agent.id, SocketAddress: "ac:" + agent.id, AgentName: agent.name,
		}); err != nil {
			t.Fatalf("Save %s: %v", agent.id, err)
		}
		tokens[agent.id] = token
	}
	return tokens
}

func TestAgentNameSelectsTheStoredCredentialInEveryCommand(t *testing.T) {
	selections := []struct {
		name      string
		reference string
		wantAgent string // "" means refused before any request
		wantErr   string
	}{
		{name: "by id", reference: "agm_pi", wantAgent: "agm_pi"},
		{name: "by exact name", reference: "Pi", wantAgent: "agm_pi"},
		{name: "by name ignoring case", reference: "cLaUdE", wantAgent: "agm_claude"},
		{name: "ambiguous name fails closed", reference: "twin", wantErr: `More than one agent in workstream 694 is called "twin". Use its ID: --agent agm_twin1 or --agent agm_twin2`},
		{name: "a name from another workstream is not used", reference: "Scout", wantErr: "Agent Scout is in workstream 610 on this machine, not 694."},
		{name: "an id from another workstream is not used", reference: "agm_scout", wantErr: "Agent agm_scout is in workstream 610 on this machine, not 694."},
		{name: "unknown name lists who is here", reference: "Nobody", wantErr: "No agent called Nobody is in workstream 694 on this machine. Agents here: Claude, Pi, Twin, Twin, agm_ghost, agm_scout."},
		{name: "another workstream's ID beats a local agent named like it", reference: "agm_scout", wantErr: "Agent agm_scout is in workstream 610 on this machine, not 694."},
		{name: "an unknown ID never falls through to a name", reference: "agm_ghost", wantErr: "No agent with ID agm_ghost is in workstream 694 on this machine."},
	}
	for family, command := range agentCommandFamilies {
		for _, sel := range selections {
			family, command, sel := family, command, sel
			t.Run(family+"/"+sel.name, func(t *testing.T) {
				t.Parallel()
				recorder := &tokenRecorder{}
				server := recorder.server(t)
				defer server.Close()
				client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22))
				client.RetryAttempts = 1
				client.ListenPollLimit = 1
				client.ListenSleep = func(time.Duration) {}
				tokens := namedAgentsMachine(t, client)

				client.Run(append(append([]string{}, command...), "--agent", sel.reference))

				recorder.mu.Lock()
				sent := append([]string(nil), recorder.tokens...)
				recorder.mu.Unlock()
				if sel.wantAgent == "" {
					if len(sent) != 0 {
						t.Fatalf("made %d requests; a refused selection must not use any credential", len(sent))
					}
					if !strings.Contains(stderr.String(), sel.wantErr) {
						t.Fatalf("stderr = %q, want %q", stderr.String(), sel.wantErr)
					}
					return
				}
				if len(sent) == 0 {
					t.Fatalf("no request made; stderr = %q", stderr.String())
				}
				for _, token := range sent {
					if token != tokens[sel.wantAgent] {
						t.Fatalf("sent another agent's credential; want %s's", sel.wantAgent)
					}
				}
			})
		}
	}
}

// Omitting --agent is unchanged: with one agent on the machine it is used, and
// with several the command asks for --agent rather than choosing.
func TestOmittedAgentBehaviourIsUnchanged(t *testing.T) {
	t.Run("the only agent on the machine is used", func(t *testing.T) {
		recorder := &tokenRecorder{}
		server := recorder.server(t)
		defer server.Close()
		client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
		token := "api_" + strings.Repeat("s", 64)
		if err := client.Store.Save(credentials.Credential{APIToken: token, SocketKey: "sock_" + strings.Repeat("0", 64),
			WorkstreamCode: "610", AgentID: "agm_scout", SocketAddress: "ac:agm_scout", AgentName: "Scout"}); err != nil {
			t.Fatal(err)
		}
		client.Run([]string{"read", "--workstream", "610"})
		if len(recorder.tokens) != 1 || recorder.tokens[0] != token {
			t.Fatalf("omitted --agent did not use the only agent; stderr = %q", stderr.String())
		}
	})
	t.Run("several agents still require --agent", func(t *testing.T) {
		recorder := &tokenRecorder{}
		server := recorder.server(t)
		defer server.Close()
		client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
		namedAgentsMachine(t, client)
		client.Run([]string{"read", "--workstream", "610"})
		if len(recorder.tokens) != 0 || !strings.Contains(stderr.String(), "Re-run for workstream 610 with --agent <agentId|name>") {
			t.Fatalf("requests = %d, stderr = %q", len(recorder.tokens), stderr.String())
		}
	})
}
