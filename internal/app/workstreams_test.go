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
	acmeID     = "org_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	betaID     = "org_bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// workstreamsTestServer serves the reads `aircom workstreams` makes: the
// organizations the machine reaches, its workstreams, and the machine's agents
// (for resolving --agent).
func workstreamsTestServer(t *testing.T, agents ...agentSummary) *httptest.Server {
	t.Helper()
	if len(agents) == 0 {
		agents = []agentSummary{
			{AgentID: leadID, Name: "Lead", OrganizationID: acmeID, WorkstreamCode: "583"},
			{AgentID: engineerID, Name: "Engineer", OrganizationID: acmeID, WorkstreamCode: "583"},
			{AgentID: watcherID, Name: "Watcher", OrganizationID: acmeID, WorkstreamCode: "345"},
		}
	}
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/organizations":
			_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case "/v1/workstreams":
			_, _ = writer.Write([]byte(`{"workstreams":[{"code":"583","name":"Budget","status":"active"},{"code":"345","name":"SCIM","status":"paused"},{"code":"610","name":"Fixes","status":"closed"}]}`))
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
				"* 583      Open    Budget  (on this machine: Engineer, Lead)\n",
				"* 345      Paused  SCIM  (on this machine: Watcher)\n",
				"  610      Closed  Fixes\n",
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
				"* 583      Open    Budget  (you are Lead here; also on this machine: Engineer)\n",
				"  345      Paused  SCIM  (on this machine: Watcher)\n",
				"  610      Closed  Fixes\n",
				"\n* marks workstreams Lead is in. Each agent joins on its own",
			},
			notWant: []string{"this workstream"},
		},
		{
			name:  "agent by id resolves the same way",
			agent: []string{"--agent", watcherID},
			want: []string{
				"* 345      Paused  SCIM  (you are Watcher here)\n",
				"  583      Open    Budget  (on this machine: Engineer, Lead)\n",
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
	server := workstreamsTestServer(t,
		agentSummary{AgentID: leadID, Name: "Lead"},
		agentSummary{AgentID: engineerID, Name: "Engineer", OrganizationID: acmeID, WorkstreamCode: "583"},
	)
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedMachine(t, client)

	// Lead exists on this machine but the service has it in no workstream.
	if exitCode := client.Run([]string{"workstreams", "--org", "Acme", "--agent", "Lead"}); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "*") || strings.Contains(out, "you are") {
		t.Fatalf("marked a workstream the caller is not in:\n%s", out)
	}
	if !strings.Contains(out, "  583      Open    Budget  (on this machine: Engineer)\n") {
		t.Fatalf("machine-mate not named:\n%s", out)
	}
	if !strings.Contains(out, "  610      Closed  Fixes\n") {
		t.Fatalf("closed workstream disappeared after the caller left:\n%s", out)
	}
	if !strings.Contains(out, "Lead is not in any of these workstreams. Each agent joins on its own") {
		t.Fatalf("no guidance for a caller in none:\n%s", out)
	}
}

func TestWorkstreamsStatusFiltersAndInvalidValue(t *testing.T) {
	server := workstreamsTestServer(t)
	defer server.Close()
	for _, tc := range []struct {
		status string
		want   string
		absent []string
	}{
		{"open", " 583      Open    Budget", []string{"SCIM", "Fixes"}},
		{"closed", " 610      Closed  Fixes", []string{"SCIM", "Budget"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
			storedMachine(t, client)
			if code := client.Run([]string{"workstreams", "--org", "Acme", "--status", tc.status}); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("missing %q: %s", tc.want, stdout.String())
			}
			for _, missing := range tc.absent {
				if strings.Contains(stdout.String(), missing) {
					t.Errorf("unexpected %q in filtered list: %s", missing, stdout.String())
				}
			}
		})
	}
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	if code := client.Run([]string{"workstreams", "--org", "Acme", "--status", "paused"}); code == 0 || !strings.Contains(stderr.String(), "--status open|closed") || stdout.Len() != 0 {
		t.Fatalf("accepted invalid filter: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestWorkstreamsFilterFindsClosedRowOnLaterPageAfterAgentLeaves(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case "/v1/agents":
			_, _ = w.Write([]byte(`{"agents":[{"agentId":"agm_11111111111111111111111111111111","name":"Lead"}]}`))
		case "/v1/workstreams":
			pages++
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"workstreams":[{"code":"583","name":"Budget","status":"active"}],"nextCursor":"page2"}`))
			} else if r.URL.Query().Get("cursor") == "page2" {
				_, _ = w.Write([]byte(`{"workstreams":[{"code":"610","name":"Fixes","status":"closed"}]}`))
			} else {
				t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
	storedMachine(t, client)
	if code := client.Run([]string{"workstreams", "--org", "Acme", "--agent", "Lead", "--status", "closed"}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if pages != 2 || !strings.Contains(stdout.String(), "  610      Closed  Fixes") || strings.Contains(stdout.String(), "Budget") {
		t.Fatalf("closed row after leave missing: pages=%d output=%s", pages, stdout.String())
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
