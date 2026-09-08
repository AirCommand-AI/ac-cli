package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const taskDetailTestResponse = `{"workstream":{"code":"694"},"tasks":[{"id":"task-1","status":"in_flight","assignee":"agent-7","title":"Build parser","description":"First line\nsecond line","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T12:00:00.000000000Z"},{"id":"task-2","status":"todo","assignee":"","title":"No comments yet","description":"","createdAt":"2026-09-08T11:00:00.000000000Z","updatedAt":"2026-09-08T11:00:00.000000000Z"}],"updates":[{"id":"update-new","taskId":"task-1","author":"Claude","body":"Newer comment","createdAt":"2026-09-08T11:30:00.000000000Z"},{"id":"update-other","taskId":"task-other","author":"Operator","body":"Unrelated","createdAt":"2026-09-08T09:00:00.000000000Z"},{"id":"update-old","taskId":"task-1","author":"Pi","body":"Older\ncomment","createdAt":"2026-09-08T10:30:00.000000000Z"}]}`

func TestTaskShowsDetailAndHandlesMissingCommentsOrTask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		taskID     string
		wantExit   int
		wantStdout string
		wantError  string
	}{
		{
			name:     "comments oldest first",
			taskID:   "task-1",
			wantExit: 0,
			wantStdout: "Title: Build parser\n" +
				"Description: First line second line\n" +
				"Status: in_flight\n" +
				"Assignee: agent-7\n" +
				"Created: 2026-09-08T10:00:00.000000000Z\n" +
				"Updated: 2026-09-08T12:00:00.000000000Z\n" +
				"Comments:\n" +
				"2026-09-08T10:30:00.000000000Z\tPi\tOlder comment\n" +
				"2026-09-08T11:30:00.000000000Z\tClaude\tNewer comment\n",
		},
		{
			name:     "no comments",
			taskID:   "task-2",
			wantExit: 0,
			wantStdout: "Title: No comments yet\n" +
				"Description: -\n" +
				"Status: todo\n" +
				"Assignee: -\n" +
				"Created: 2026-09-08T11:00:00.000000000Z\n" +
				"Updated: 2026-09-08T11:00:00.000000000Z\n" +
				"Comments:\n" +
				"No comments for task task-2.\n",
		},
		{
			name:      "unknown id",
			taskID:    "task-missing",
			wantExit:  1,
			wantError: "Task task-missing was not found in workstream 694.",
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
				_, _ = writer.Write([]byte(taskDetailTestResponse))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", nil)
			saveTestCredential(t, client, credential)
			if exitCode := client.Run([]string{"task", test.taskID, "--workstream", "694"}); exitCode != test.wantExit {
				t.Fatalf("task exit code = %d, want %d; stderr = %q", exitCode, test.wantExit, stderr.String())
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want one detail request", requests)
			}
			if got := stdout.String(); got != test.wantStdout {
				t.Fatalf("stdout = %q, want %q", got, test.wantStdout)
			}
			if test.wantError != "" && !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.wantError)
			}
		})
	}
}

func TestTaskPositionalArgumentValidationUsesUsageWithoutRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
	}{
		{name: "missing id", arguments: []string{"task"}},
		{name: "flag where id belongs", arguments: []string{"task", "--workstream", "694"}},
		{name: "missing workstream", arguments: []string{"task", "task-1"}},
		{name: "extra before flags", arguments: []string{"task", "task-1", "extra", "--workstream", "694"}},
		{name: "extra after flags", arguments: []string{"task", "task-1", "--workstream", "694", "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", nil)
			if exitCode := client.Run(test.arguments); exitCode == 0 {
				t.Fatal("task unexpectedly accepted invalid positional arguments")
			}
			if requests != 0 {
				t.Fatalf("invalid arguments made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), taskUsage) {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestDecodeTaskDetailRejectsIncompleteFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "missing updates", body: `{"tasks":[]}`},
		{name: "missing task times", body: `{"tasks":[{"id":"task-1","status":"todo","title":"Task"}],"updates":[]}`},
		{name: "invalid update", body: `{"tasks":[],"updates":[{"id":"update-1","body":"Comment"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeTaskDetail([]byte(test.body)); err == nil {
				t.Fatal("decodeTaskDetail unexpectedly accepted incomplete detail")
			}
		})
	}
}
