package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const revokedSessionBody = `{"error":"Unauthorized","code":"SessionRevoked","reason":"agent_stopped","revokedBy":"owner@example.test","revokedAt":"2026-09-26T13:41:44.000000000Z"}`

func revokedTestCredential() credentials.Credential {
	credential := testCredential()
	credential.OrganizationID = "org_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	return credential
}

func wantStoppedRecovery() string {
	return "Agent was stopped by owner@example.test at 2026-09-26T13:41:44.000000000Z. Ask a person to resume it from the AirCommand dashboard."
}

func TestLifecycleRecoveryUsesHumanReasonsAndOnlyStoppedRemovedBlockLeaveJoin(t *testing.T) {
	credential := revokedTestCredential()
	for _, test := range []struct {
		name, reason, want string
		commands           bool
	}{
		{name: "stopped", reason: "agent_stopped", want: "Agent was stopped by Ada at 2026-09-26T13:41:44Z. Ask a person to resume it from the AirCommand dashboard."},
		{name: "removed", reason: "agent_removed", want: "Agent was removed by Ada at 2026-09-26T13:41:44Z. Ask a person to add it back from the AirCommand dashboard."},
		{name: "left", reason: "agent_left", want: "Agent access was left by Ada at 2026-09-26T13:41:44Z. To recover, run:", commands: true},
		{name: "unknown", reason: "some_internal_code", want: "Agent access was revoked by Ada at 2026-09-26T13:41:44Z. To recover, run:", commands: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"code":"SessionRevoked","reason":"` + test.reason + `","revokedBy":"Ada","revokedAt":"2026-09-26T13:41:44Z"}`)
			got := revokedSessionRecoveryError(body, credential).Error()
			if !strings.Contains(got, test.want) {
				t.Fatalf("recovery = %q, want %q", got, test.want)
			}
			if strings.Contains(got, test.reason) {
				t.Fatalf("recovery leaked raw reason %q: %q", test.reason, got)
			}
			if test.commands != strings.Contains(got, "aircom leave --agent") {
				t.Fatalf("commands=%v recovery=%q", test.commands, got)
			}
		})
	}
}

func TestJoinPrintsStoppedOrRemovedDetailsFromConflict(t *testing.T) {
	for _, test := range []struct{ code, action, actorKey, timeKey string }{
		{code: "AgentStopped", action: "stopped", actorKey: "stoppedBy", timeKey: "stoppedAt"},
		{code: "AgentRemoved", action: "removed", actorKey: "removedBy", timeKey: "removedAt"},
	} {
		t.Run(test.action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/v1/organizations":
					_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
				case "/v1/agents":
					_, _ = writer.Write([]byte(`{"agents":[{"agentId":"agent-7","name":"Builder","status":"connected"}]}`))
				case "/v1/agents/agent-7/workstreams/694":
					writer.WriteHeader(http.StatusConflict)
					_, _ = writer.Write([]byte(`{"code":"` + test.code + `","details":{"` + test.actorKey + `":"Ada","` + test.timeKey + `":"2026-09-26T13:41:44Z"}}`))
				default:
					t.Errorf("unexpected path %q", request.URL.Path)
				}
			}))
			defer server.Close()
			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
			storedMachine(t, client)
			if exitCode := client.Run([]string{"join", "--agent", "Builder", "--org", "Acme", "--workstream", "694"}); exitCode == 0 {
				t.Fatal("join accepted terminal lifecycle conflict")
			}
			if stdout.Len() != 0 {
				t.Fatalf("join stdout = %q", stdout.String())
			}
			action := "resume it"
			if test.action == "removed" {
				action = "add it back"
			}
			want := "Agent was " + test.action + " by Ada at 2026-09-26T13:41:44Z. Ask a person to " + action + " from the AirCommand dashboard.\n"
			if got := stderr.String(); got != want {
				t.Fatalf("join stderr = %q, want %q", got, want)
			}
		})
	}
}

func TestListenerReloadsChangedCredentialAfterUnauthorized(t *testing.T) {
	credential := revokedTestCredential()
	replacement := credential
	replacement.APIToken = "api_" + repeatedHex(0xc3)
	var client *App
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/agent/v1/workstreams/694/notifications" {
			t.Errorf("path = %q", request.URL.Path)
		}
		switch requests {
		case 1:
			if got := request.Header.Get("Authorization"); got != "Bearer "+credential.APIToken {
				t.Errorf("first bearer = %q", got)
			}
			_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"c0","pollAfterSeconds":5}`))
		case 2:
			if got := request.Header.Get("Authorization"); got != "Bearer "+credential.APIToken {
				t.Errorf("revoked bearer = %q", got)
			}
			if err := client.Store.Save(replacement); err != nil {
				t.Errorf("replace credential: %v", err)
			}
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(revokedSessionBody))
		case 3:
			if got := request.Header.Get("Authorization"); got != "Bearer "+replacement.APIToken {
				t.Errorf("replacement bearer = %q", got)
			}
			_, _ = writer.Write([]byte(`{"notifications":[],"cursor":"c1","pollAfterSeconds":5}`))
		default:
			t.Errorf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client, stdout, stderr := listenerApp(server.URL, t.TempDir())
	if err := client.Store.Save(credential); err != nil {
		t.Fatal(err)
	}
	client.ListenPollLimit = 2
	client.ListenSleep = func(time.Duration) {}
	if exitCode := client.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode != 0 {
		t.Fatalf("listen exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want initial poll, 401, and one refreshed retry", requests)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("successful credential reload wrote stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestListenerRevocationOutputTimestampsEveryRecoveryLine(t *testing.T) {
	credential := revokedTestCredential()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(revokedSessionBody))
	}))
	defer server.Close()

	client, stdout, _ := listenerApp(server.URL, t.TempDir())
	if err := client.Store.Save(credential); err != nil {
		t.Fatal(err)
	}
	client.ListenNow = func() time.Time { return time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC) }
	if exitCode := client.Run([]string{"listen", "--workstream", "694", "--agent", credential.AgentID}); exitCode == 0 {
		t.Fatal("revoked listener exited successfully")
	}
	const timestamp = "2026-09-26T14:00:00Z [AirCommand] "
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, timestamp) {
			t.Fatalf("listener line lacks timestamp: %q", line)
		}
	}
	got := stripListenerTimestamps(stdout.String())
	if !strings.Contains(got, wantStoppedRecovery()) {
		t.Fatalf("recovery output = %q, missing %q", got, wantStoppedRecovery())
	}
	if strings.Contains(got, "aircom leave") || strings.Contains(got, "aircom join") {
		t.Fatalf("stopped recovery incorrectly suggests a bypass: %q", got)
	}
}

func TestJoinAlreadyThereRejectsARevokedStoredToken(t *testing.T) {
	credential := revokedTestCredential()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/organizations":
			_, _ = writer.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case "/v1/agents":
			_, _ = writer.Write([]byte(`{"agents":[{"agentId":"agent-7","name":"Builder","status":"connected","organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","workstreamCode":"694"}]}`))
		case "/agent/v1/workstreams/694":
			if got := request.Header.Get("Authorization"); got != "Bearer "+credential.APIToken {
				t.Errorf("stored bearer = %q", got)
			}
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(revokedSessionBody))
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	storedMachine(t, client)
	saveTestCredential(t, client, credential)
	if exitCode := client.Run([]string{"join", "--agent", "Builder", "--org", "Acme", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("join accepted a revoked stored token")
	}
	if stdout.Len() != 0 {
		t.Fatalf("revoked join wrote success output %q", stdout.String())
	}
	if got, want := stderr.String(), wantStoppedRecovery()+"\n"; got != want {
		t.Fatalf("join recovery = %q, want %q", got, want)
	}
}

func TestRevokedSessionUsesTheSameRecoveryForInboxSendTaskAndTasks(t *testing.T) {
	credential := revokedTestCredential()
	commands := [][]string{
		{"inbox", "--workstream", "694"},
		{"send", "--workstream", "694", "--to", "agm_0123456789abcdef0123456789abcdef", "--body", "hello"},
		{"task", "task-1", "--workstream", "694"},
		{"tasks", "--workstream", "694"},
	}
	for _, command := range commands {
		command := command
		t.Run(command[0], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("Authorization"); got != "Bearer "+credential.APIToken {
					t.Errorf("bearer = %q", got)
				}
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(revokedSessionBody))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11))
			saveTestCredential(t, client, credential)
			if exitCode := client.Run(command); exitCode == 0 {
				t.Fatalf("%v accepted a revoked session", command)
			}
			if stdout.Len() != 0 {
				t.Fatalf("%v wrote stdout %q", command, stdout.String())
			}
			if got, want := stderr.String(), wantStoppedRecovery()+"\n"; got != want {
				t.Fatalf("%v recovery = %q, want %q", command, got, want)
			}
		})
	}
}
