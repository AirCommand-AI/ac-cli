package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	leadID     = "agm_11111111111111111111111111111111"
	engineerID = "agm_22222222222222222222222222222222"
	watcherID  = "agm_33333333333333333333333333333333"
)

// workstreamsTestServer serves the reads `aircom workstreams` makes: the
// organizations the machine reaches, its workstreams, and the machine's agents
// (for resolving --agent).
func workstreamsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	agents := []agentSummary{
		{AgentID: leadID, Name: "Lead", WorkstreamCode: "583"},
		{AgentID: engineerID, Name: "Engineer", WorkstreamCode: "583"},
		{AgentID: watcherID, Name: "Watcher", WorkstreamCode: "345"},
	}
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/organizations":
			_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case "/v1/workstreams":
			_, _ = writer.Write([]byte(`{"workstreams":[{"code":"583","name":"Budget"},{"code":"345","name":"SCIM"},{"code":"610","name":"Fixes"}]}`))
		case "/v1/agents":
			body, _ := json.Marshal(listAgentsResponse{Agents: agents})
			_, _ = writer.Write(body)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
}

func TestWorkstreamsNamesTheCallerOnlyWhenAsked(t *testing.T) {
	cases := []struct {
		name    string
		agent   []string
		want    []string
		notWant []string
	}{
		{
			name: "no agent: every local agent is on this machine, nobody is you",
			want: []string{
				"* 583      Budget  (on this machine: Engineer, Lead)\n",
				"* 345      SCIM  (on this machine: Watcher)\n",
				"  610      Fixes\n",
				// Two workstreams are marked, so the footnote explains the marker
				// rather than speaking of a single workstream.
				"\n* marks workstreams with an agent from this machine. Each agent joins on its own",
			},
			notWant: []string{"you are", "join is only needed", "this workstream"},
		},
		{
			name:  "agent by name: only that agent is you",
			agent: []string{"--agent", "Lead"},
			want: []string{
				"* 583      Budget  (you are Lead here; also on this machine: Engineer)\n",
				"  345      SCIM  (on this machine: Watcher)\n",
				"  610      Fixes\n",
				"\n* marks workstreams Lead is in. Each agent joins on its own",
			},
			notWant: []string{"this workstream"},
		},
		{
			name:  "agent by id resolves the same way",
			agent: []string{"--agent", watcherID},
			want: []string{
				"* 345      SCIM  (you are Watcher here)\n",
				"  583      Budget  (on this machine: Engineer, Lead)\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := workstreamsTestServer(t)
			defer server.Close()
			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
			storedAgent(t, client, leadID, "583", "Lead")
			storedAgent(t, client, engineerID, "583", "Engineer")
			storedAgent(t, client, watcherID, "345", "Watcher")

			arguments := append([]string{"workstreams", "--org", "Acme"}, tc.agent...)
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("output missing %q:\n%s", want, stdout.String())
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(stdout.String(), notWant) {
					t.Errorf("output contains %q:\n%s", notWant, stdout.String())
				}
			}
		})
	}
}

func TestWorkstreamsSaysWhenTheCallerIsInNone(t *testing.T) {
	server := workstreamsTestServer(t)
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedAgent(t, client, engineerID, "583", "Engineer")

	// Lead exists on this machine but has no credential in any listed workstream.
	if exitCode := client.Run([]string{"workstreams", "--org", "Acme", "--agent", "Lead"}); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "*") || strings.Contains(out, "you are") {
		t.Fatalf("marked a workstream the caller is not in:\n%s", out)
	}
	if !strings.Contains(out, "  583      Budget  (on this machine: Engineer)\n") {
		t.Fatalf("machine-mate not named:\n%s", out)
	}
	if !strings.Contains(out, "Lead is not in any of these workstreams. Each agent joins on its own") {
		t.Fatalf("no guidance for a caller in none:\n%s", out)
	}
}

func TestWorkstreamsRefusesAnUnknownAgent(t *testing.T) {
	server := workstreamsTestServer(t)
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedMachine(t, client)

	if exitCode := client.Run([]string{"workstreams", "--org", "Acme", "--agent", "Nobody"}); exitCode == 0 {
		t.Fatal("listed workstreams for an agent that does not exist")
	}
	if stdout.Len() != 0 {
		t.Fatalf("printed a listing before refusing: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Nobody") {
		t.Fatalf("refusal does not name the reference: %q", stderr.String())
	}
}
