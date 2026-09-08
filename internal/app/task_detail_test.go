package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const taskDetailTestResponse = `{"workstream":{"code":"694"},"tasks":[{"id":"task-1","status":"in_flight","assignee":"agent-7","title":"Build parser","description":"First line\nsecond line","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T12:00:00.000000000Z"},{"id":"task-2","status":"todo","assignee":"","title":"No comments yet","description":"","createdAt":"2026-09-08T11:00:00.000000000Z","updatedAt":"2026-09-08T11:00:00.000000000Z"}],"updates":[{"id":"update-new","taskId":"task-1","author":"Claude","body":"Newer comment","createdAt":"2026-09-08T11:30:00.000000000Z"},{"id":"update-other","taskId":"task-other","author":"Operator","body":"Unrelated","createdAt":"2026-09-08T09:00:00.000000000Z"},{"id":"update-old","taskId":"task-1","author":"Pi","body":"Older\ncomment","createdAt":"2026-09-08T10:30:00.000000000Z"}]}`

func TestTaskWithoutStatusShowsDetailAndHandlesMissingCommentsOrTask(t *testing.T) {
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

func TestTaskStatusRejectsInvalidValueBeforeRequest(t *testing.T) {
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

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--status", status}); exitCode == 0 {
				t.Fatal("task unexpectedly accepted an invalid status")
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

func TestTaskStatusRetriesRetryableHTTPWithOneIdempotencyID(t *testing.T) {
	t.Parallel()

	for _, retryableStatus := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		retryableStatus := retryableStatus
		t.Run(http.StatusText(retryableStatus), func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			idempotencyID := repeatedHex(0x44)
			wantBody, err := json.Marshal(taskStatusRequest{Status: "landed", IdempotencyID: idempotencyID})
			if err != nil {
				t.Fatal(err)
			}
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPatch || request.URL.RequestURI() != "/agent/v1/workstreams/694/tasks/task-1" {
					t.Errorf("request = %s %s, want task PATCH", request.Method, request.URL.RequestURI())
				}
				if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
				body, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					t.Errorf("read body: %v", readErr)
				}
				bodies = append(bodies, body)
				if len(bodies) == 1 {
					writer.WriteHeader(retryableStatus)
					return
				}
				_, _ = writer.Write([]byte(`{"id":"task-1","status":"landed","assignee":"agent-7","title":"Build parser","description":"Done","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T13:00:00.000000000Z"}`))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
			client.RetryAttempts = 2
			saveTestCredential(t, client, credential)
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--status", "landed"}); exitCode != 0 {
				t.Fatalf("task status exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want 2", len(bodies))
			}
			for index, body := range bodies {
				if !bytes.Equal(body, wantBody) {
					t.Errorf("request %d body = %s, want %s", index+1, body, wantBody)
				}
			}
			if !strings.Contains(stdout.String(), "Status: landed\n") {
				t.Fatalf("stdout = %q, want updated state", stdout.String())
			}
		})
	}
}

func TestTaskStatusRetriesTransportWithTheSameRequestBody(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	idempotencyID := repeatedHex(0x44)
	wantBody, err := json.Marshal(taskStatusRequest{Status: "blocked", IdempotencyID: idempotencyID})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":"task-1","status":"blocked","assignee":"agent-7","title":"Build parser","description":"Waiting","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T13:00:00.000000000Z"}`))
	}))
	defer server.Close()

	transport := &failOnceTransport{base: http.DefaultTransport}
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
	client.HTTPClient = &http.Client{Transport: transport}
	client.RetryAttempts = 2
	saveTestCredential(t, client, credential)
	if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--status", "blocked"}); exitCode != 0 {
		t.Fatalf("task status exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if transport.calls != 2 || len(transport.bodies) != 2 {
		t.Fatalf("transport calls = %d, bodies = %d; want 2, 2", transport.calls, len(transport.bodies))
	}
	for index, body := range transport.bodies {
		if !bytes.Equal(body, wantBody) {
			t.Errorf("transport request %d body = %s, want %s", index+1, body, wantBody)
		}
	}
	if !strings.Contains(stdout.String(), "Status: blocked\n") {
		t.Fatalf("stdout = %q, want updated state", stdout.String())
	}
}

func TestTaskStatusDoesNotRetryFinalHTTPStatuses(t *testing.T) {
	t.Parallel()

	for _, finalStatus := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity} {
		finalStatus := finalStatus
		t.Run(http.StatusText(finalStatus), func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++
				writer.WriteHeader(finalStatus)
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--status", "blocked"}); exitCode == 0 {
				t.Fatal("task status unexpectedly accepted a final error")
			}
			if requests != 1 {
				t.Fatalf("final HTTP %d made %d requests, want 1", finalStatus, requests)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
			if finalStatus == http.StatusNotFound {
				for _, want := range []string{"task-1", "694"} {
					if !strings.Contains(stderr.String(), want) {
						t.Errorf("not-found error %q does not contain %q", stderr.String(), want)
					}
				}
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
