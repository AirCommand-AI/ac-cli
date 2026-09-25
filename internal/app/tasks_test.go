package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const taskListTestResponse = `{"workstream":{"code":"694"},"tasks":[{"id":"task-1","status":"todo","assignee":"agent-7","title":"Own todo"},{"id":"task-2","status":"landed","assignee":"agent-other","title":"Other landed"},{"id":"task-3","status":"blocked","assignee":"","title":"Unassigned"},{"id":"task-4","status":"landed","assignee":"agent-7","title":"Own\nlanded"}],"updates":[]}`

func TestTasksListsAndFiltersWorkstreamDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "all tasks",
			want: "task-1\ttodo\tagent-7\tOwn todo\n" +
				"task-2\tlanded\tagent-other\tOther landed\n" +
				"task-3\tblocked\t-\tUnassigned\n" +
				"task-4\tlanded\tagent-7\tOwn landed\n",
		},
		{
			name: "mine",
			args: []string{"--mine"},
			want: "task-1\ttodo\tagent-7\tOwn todo\n" +
				"task-4\tlanded\tagent-7\tOwn landed\n",
		},
		{
			name: "status",
			args: []string{"--status", "landed"},
			want: "task-2\tlanded\tagent-other\tOther landed\n" +
				"task-4\tlanded\tagent-7\tOwn landed\n",
		},
		{
			name: "mine and status",
			args: []string{"--mine", "--status", "landed"},
			want: "task-4\tlanded\tagent-7\tOwn landed\n",
		},
		{
			name: "no status matches",
			args: []string{"--status", "in_flight"},
			want: "No tasks with status in_flight in workstream 694.\n",
		},
		{
			name: "no combined matches",
			args: []string{"--mine", "--status", "blocked"},
			want: "No tasks assigned to this agent with status blocked in workstream 694.\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.Method != http.MethodGet || request.URL.RequestURI() != "/agent/v1/workstreams/694" {
					t.Errorf("request = %s %s, want detail GET", request.Method, request.URL.RequestURI())
				}
				if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
				_, _ = writer.Write([]byte(taskListTestResponse))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", nil)
			saveTestCredential(t, client, credential)
			arguments := append([]string{"tasks", "--workstream", "694"}, test.args...)
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("tasks exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
			if got := stdout.String(); got != test.want {
				t.Fatalf("stdout = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTasksExplainsEmptyResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unfiltered", want: "No tasks in workstream 694.\n"},
		{name: "mine", args: []string{"--mine"}, want: "No tasks assigned to this agent in workstream 694.\n"},
		{name: "status", args: []string{"--status", "todo"}, want: "No tasks with status todo in workstream 694.\n"},
		{name: "combined", args: []string{"--mine", "--status", "todo"}, want: "No tasks assigned to this agent with status todo in workstream 694.\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++
				_, _ = writer.Write([]byte(`{"workstream":{"code":"694"},"tasks":[],"updates":[]}`))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", nil)
			saveTestCredential(t, client, testCredential())
			arguments := append([]string{"tasks", "--workstream", "694"}, test.args...)
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("tasks exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
			if got := stdout.String(); got != test.want {
				t.Fatalf("stdout = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTasksRejectsInvalidStatusBeforeRequest(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"doing", "TODO", "in-flight", " todo "} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", nil)
			if exitCode := client.Run([]string{"tasks", "--workstream", "694", "--status", status}); exitCode == 0 {
				t.Fatal("tasks unexpectedly accepted an invalid status")
			}
			if requests != 0 {
				t.Fatalf("invalid status made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "todo, in_flight, blocked, or landed") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTasksAgentSelectionFailsClosedAndMineUsesSelectedAgent(t *testing.T) {
	t.Parallel()

	claude := testCredential()
	claude.AgentID = "agent-claude"
	claude.APIToken = "api_" + repeatedHex(0xc1)
	claude.SocketKey = "sock_" + repeatedHex(0xc2)
	pi := testCredential()
	pi.AgentID = "agent-pi"
	pi.APIToken = "api_" + repeatedHex(0xd1)
	pi.SocketKey = "sock_" + repeatedHex(0xd2)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got, want := request.Header.Get("Authorization"), "Bearer "+pi.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		_, _ = writer.Write([]byte(`{"workstream":{"code":"694"},"tasks":[{"id":"task-pi","status":"in_flight","assignee":"agent-pi","title":"Selected"},{"id":"task-claude","status":"todo","assignee":"agent-claude","title":"Other"}],"updates":[]}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	for _, credential := range []credentials.Credential{claude, pi} {
		saveTestCredential(t, client, credential)
	}
	if exitCode := client.Run([]string{"tasks", "--workstream", "694", "--mine"}); exitCode == 0 {
		t.Fatal("tasks guessed between enrolled agents")
	}
	if requests != 0 {
		t.Fatalf("ambiguous selection made %d requests, want 0", requests)
	}
	for _, want := range []string{"agent-claude", "agent-pi", "--agent <agentId|name>"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("selection error %q does not contain %q", stderr.String(), want)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := client.Run([]string{"tasks", "--workstream", "694", "--agent", "agent-pi", "--mine"}); exitCode != 0 {
		t.Fatalf("selected tasks exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("selected tasks made %d requests, want 1", requests)
	}
	if got, want := stdout.String(), "task-pi\tin_flight\tagent-pi\tSelected\n"; got != want {
		t.Fatalf("selected stdout = %q, want %q", got, want)
	}
}

func TestDecodeTaskListRejectsMalformedDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "missing tasks", body: `{}`},
		{name: "null tasks", body: `{"tasks":null}`},
		{name: "missing id", body: `{"tasks":[{"status":"todo","title":"No ID"}]}`},
		{name: "invalid status", body: `{"tasks":[{"id":"task-1","status":"doing","title":"Bad"}]}`},
		{name: "trailing json", body: `{"tasks":[]} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeTaskList([]byte(test.body)); err == nil {
				t.Fatal("decodeTaskList unexpectedly accepted malformed detail")
			}
		})
	}
}
