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

func TestTaskCommentPostsOnlyTaskScopedUpdateAndPrintsConfirmation(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	commentBody := "  Finished\nwith tests  "
	idempotencyID := repeatedHex(0x55)
	wantBody, err := json.Marshal(taskCommentRequest{Body: commentBody, TaskID: "task-1", IdempotencyID: idempotencyID})
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := json.Marshal(taskCommentItem{
		ID: "update-1", TaskID: "task-1", Author: "Builder", Body: commentBody,
		CreatedAt: "2026-09-08T14:00:00.000000000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/agent/v1/workstreams/694/updates" {
			t.Errorf("request = %s %s, want task update POST", request.Method, request.URL.RequestURI())
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read body: %v", readErr)
		}
		if !bytes.Equal(body, wantBody) {
			t.Errorf("request body = %s, want %s", body, wantBody)
		}
		if bytes.Contains(body, []byte(`"status"`)) || bytes.Contains(body, []byte(`"title"`)) || bytes.Contains(body, []byte(`"description"`)) {
			t.Errorf("comment request attempted to mutate task fields: %s", body)
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x55))
	saveTestCredential(t, client, credential)
	if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--comment", commentBody}); exitCode != 0 {
		t.Fatalf("task comment exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one POST and no task PATCH/GET", requests)
	}
	wantOutput := "Comment added: update-1\n" +
		"Task: task-1\n" +
		"Author: Builder\n" +
		"Created: 2026-09-08T14:00:00.000000000Z\n" +
		"Body:   Finished with tests  \n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestTaskCommentRejectsBlankTextBeforeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		comment string
	}{
		{name: "empty", comment: ""},
		{name: "spaces", comment: "   "},
		{name: "control whitespace", comment: "\n\t"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x55))
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--comment", test.comment}); exitCode == 0 {
				t.Fatal("task unexpectedly accepted a blank comment")
			}
			if requests != 0 {
				t.Fatalf("blank comment made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "non-whitespace text") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskCommentAndStatusAreRejectedTogetherBeforeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		flags []string
	}{
		{name: "comment then status", flags: []string{"--comment", "done", "--status", "landed"}},
		{name: "status then comment", flags: []string{"--status", "landed", "--comment", "done"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x55))
			arguments := append([]string{"task", "task-1", "--workstream", "694"}, test.flags...)
			if exitCode := client.Run(arguments); exitCode == 0 {
				t.Fatal("task unexpectedly accepted comment and status together")
			}
			if requests != 0 {
				t.Fatalf("combined mutations made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "cannot be used together") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskCommentRetryableStatusesReuseOneServerIdempotencyID(t *testing.T) {
	t.Parallel()

	for _, retryableStatus := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		retryableStatus := retryableStatus
		t.Run(http.StatusText(retryableStatus), func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			idempotencyID := repeatedHex(0x55)
			wantBody, err := json.Marshal(taskCommentRequest{Body: "retry safely", TaskID: "task-1", IdempotencyID: idempotencyID})
			if err != nil {
				t.Fatal(err)
			}
			responseBody, err := json.Marshal(taskCommentItem{
				ID: "update-retry", TaskID: "task-1", Author: "Builder", Body: "retry safely",
				CreatedAt: "2026-09-08T14:00:00.000000000Z",
			})
			if err != nil {
				t.Fatal(err)
			}
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					t.Errorf("read body: %v", readErr)
				}
				bodies = append(bodies, body)
				if len(bodies) == 1 {
					writer.WriteHeader(retryableStatus)
					return
				}
				writer.WriteHeader(http.StatusCreated)
				_, _ = writer.Write(responseBody)
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x55))
			client.RetryAttempts = 2
			saveTestCredential(t, client, credential)
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--comment", "retry safely"}); exitCode != 0 {
				t.Fatalf("task comment exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want 2", len(bodies))
			}
			for index, body := range bodies {
				if !bytes.Equal(body, wantBody) {
					t.Errorf("request %d body = %s, want %s", index+1, body, wantBody)
				}
			}
			if !strings.Contains(stdout.String(), "Comment added: update-retry\n") {
				t.Fatalf("stdout = %q, want confirmation", stdout.String())
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

func TestTaskAssigneeHandsTheTaskOn(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	var got []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.RequestURI() != "/agent/v1/workstreams/694/tasks/task-1" {
			t.Errorf("request = %s %s, want task PATCH", request.Method, request.URL.RequestURI())
		}
		got, _ = io.ReadAll(request.Body)
		_, _ = writer.Write([]byte(`{"id":"task-1","status":"todo","assignee":"agm_2","title":"Build parser","description":"","createdAt":"2026-09-08T10:00:00Z","updatedAt":"2026-09-08T13:00:00Z","createdBy":{"nature":"agent","id":"agm_1","name":"Lead"},"assignedBy":{"nature":"agent","id":"agm_1","name":"Lead"},"assignedAt":"2026-09-08T13:00:00Z"}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
	saveTestCredential(t, client, credential)
	if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--assignee", "Engineer"}); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if string(got) != `{"assignee":"Engineer","idempotencyId":"`+repeatedHex(0x44)+`"}` {
		t.Fatalf("body = %s", got)
	}
	for _, want := range []string{"Assignee: agm_2\n", "Created by: Lead\n", "Assigned by: Lead at 2026-09-08T13:00:00Z\n"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTaskAssigneeIsRefusedWithOtherChangesOrBlank(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		arguments []string
		want      string
	}{
		{"with status", []string{"--assignee", "Engineer", "--status", "landed"}, "cannot be combined"},
		{"with comment", []string{"--assignee", "Engineer", "--comment", "hi"}, "cannot be combined"},
		{"blank", []string{"--assignee", " "}, "must name an agent"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
			arguments := append([]string{"task", "task-1", "--workstream", "694"}, tc.arguments...)
			if exitCode := client.Run(arguments); exitCode == 0 {
				t.Fatal("accepted")
			}
			if requests != 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("requests = %d, stderr = %q", requests, stderr.String())
			}
		})
	}
}

func TestTaskAssigneeRetriesReuseOneIdempotencyID(t *testing.T) {
	t.Parallel()

	for _, retryableStatus := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		retryableStatus := retryableStatus
		t.Run(http.StatusText(retryableStatus), func(t *testing.T) {
			t.Parallel()

			wantBody, err := json.Marshal(taskAssigneeRequest{Assignee: "Engineer", IdempotencyID: repeatedHex(0x55)})
			if err != nil {
				t.Fatal(err)
			}
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				bodies = append(bodies, body)
				if len(bodies) == 1 {
					writer.WriteHeader(retryableStatus)
					return
				}
				_, _ = writer.Write([]byte(`{"id":"task-1","status":"todo","assignee":"agm_2","title":"Build parser","description":"","createdAt":"2026-09-08T10:00:00Z","updatedAt":"2026-09-08T13:00:00Z"}`))
			}))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x55))
			client.RetryAttempts = 2
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--assignee", "Engineer"}); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want the failure and one retry", len(bodies))
			}
			for i, body := range bodies {
				if !bytes.Equal(body, wantBody) {
					t.Errorf("request %d body = %s, want %s (one key for the whole invocation)", i+1, body, wantBody)
				}
			}
		})
	}
}

func TestTaskAssigneeRefusesWithoutAnIdempotencyID(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()

	// No randomness available: the key cannot be generated.
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom())
	saveTestCredential(t, client, testCredential())
	if exitCode := client.Run([]string{"task", "task-1", "--workstream", "694", "--assignee", "Engineer"}); exitCode == 0 {
		t.Fatal("reassigned without an idempotency key")
	}
	if requests != 0 {
		t.Fatalf("made %d requests without a key, want 0", requests)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Unable to generate a task reassignment idempotency ID.") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}
