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

func TestTaskCreateKeywordPrecedenceAndLiteralIDEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		arguments        []string
		saveCredential   bool
		wantRequests     int
		wantMethod       string
		wantOutput       string
		wantErrorContain string
	}{
		{
			name:             "create token selects create and requires title",
			arguments:        []string{"task", "create", "--workstream", "694"},
			wantErrorContain: taskCreateUsage,
		},
		{
			name:           "explicit id reaches literal create task",
			arguments:      []string{"task", "--id", "create", "--workstream", "694"},
			saveCredential: true,
			wantRequests:   1,
			wantMethod:     http.MethodGet,
			wantOutput:     "Title: Literal create task\n",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.Method != test.wantMethod {
					t.Errorf("method = %s, want %s", request.Method, test.wantMethod)
				}
				if request.URL.RequestURI() != "/agent/v1/workstreams/694" {
					t.Errorf("path = %s, want workstream detail", request.URL.RequestURI())
				}
				_, _ = writer.Write([]byte(`{"tasks":[{"id":"create","status":"todo","assignee":"","title":"Literal create task","description":"","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T10:00:00.000000000Z"}],"updates":[]}`))
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x66))
			if test.saveCredential {
				saveTestCredential(t, client, testCredential())
			}
			exitCode := client.Run(test.arguments)
			if test.wantErrorContain == "" {
				if exitCode != 0 {
					t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
				}
				if !strings.Contains(stdout.String(), test.wantOutput) {
					t.Fatalf("stdout = %q, want content %q", stdout.String(), test.wantOutput)
				}
			} else {
				if exitCode == 0 || !strings.Contains(stderr.String(), test.wantErrorContain) {
					t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
				}
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want empty", stdout.String())
				}
			}
			if requests != test.wantRequests {
				t.Fatalf("requests = %d, want %d", requests, test.wantRequests)
			}
		})
	}
}

func TestTaskCreateRejectsBlankTitlesBeforeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		titleArguments   []string
		wantErrorContain string
	}{
		{name: "missing flag", wantErrorContain: taskCreateUsage},
		{name: "empty", titleArguments: []string{"--title", ""}, wantErrorContain: "non-whitespace text"},
		{name: "whitespace", titleArguments: []string{"--title", " \n\t "}, wantErrorContain: "non-whitespace text"},
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

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x66))
			arguments := append([]string{"task", "create", "--workstream", "694"}, test.titleArguments...)
			if exitCode := client.Run(arguments); exitCode == 0 {
				t.Fatal("task create unexpectedly accepted a blank title")
			}
			if requests != 0 {
				t.Fatalf("blank title made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.wantErrorContain) {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskCreatePayloadDefaultsAndOptionalFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		wantRequest taskCreateRequest
		createdID   string
	}{
		{
			name:      "defaults status and assignee",
			arguments: []string{"--title", "Write tests"},
			wantRequest: taskCreateRequest{
				Title: "Write tests", Status: "todo", IdempotencyID: repeatedHex(0x66),
			},
			createdID: "task-default",
		},
		{
			name:      "sends optional fields and explicit status",
			arguments: []string{"--title", "Ship feature", "--description", "Review then merge", "--assignee", "Builder", "--status", "in_flight"},
			wantRequest: taskCreateRequest{
				Title: "Ship feature", Description: "Review then merge", Assignee: "Builder",
				Status: "in_flight", IdempotencyID: repeatedHex(0x66),
			},
			createdID: "task-explicit",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			wantBody, err := json.Marshal(test.wantRequest)
			if err != nil {
				t.Fatal(err)
			}
			responseBody, err := json.Marshal(taskListItem{
				ID: test.createdID, Status: test.wantRequest.Status, Assignee: "agm-resolved",
				Title: test.wantRequest.Title, Description: test.wantRequest.Description,
				CreatedAt: "2026-09-08T15:00:00.000000000Z", UpdatedAt: "2026-09-08T15:00:00.000000000Z",
			})
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.Method != http.MethodPost || request.URL.RequestURI() != "/agent/v1/workstreams/694/tasks" {
					t.Errorf("request = %s %s, want task create POST", request.Method, request.URL.RequestURI())
				}
				if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
				body, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					t.Errorf("read body: %v", readErr)
				}
				if !bytes.Equal(body, wantBody) {
					t.Errorf("request body = %s, want %s", body, wantBody)
				}
				writer.WriteHeader(http.StatusCreated)
				_, _ = writer.Write(responseBody)
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x66))
			saveTestCredential(t, client, credential)
			arguments := append([]string{"task", "create", "--workstream", "694"}, test.arguments...)
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("task create exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
			if got, want := stdout.String(), "Created task: "+test.createdID+"\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
		})
	}
}

func TestTaskCreateRejectsInvalidStatusBeforeRequest(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"", "doing", "TODO", " todo "} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x66))
			arguments := []string{"task", "create", "--workstream", "694", "--title", "Valid title", "--status", status}
			if exitCode := client.Run(arguments); exitCode == 0 {
				t.Fatal("task create unexpectedly accepted invalid status")
			}
			if requests != 0 {
				t.Fatalf("invalid status made %d requests, want 0", requests)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "todo, in_flight, blocked, landed, or cancelled") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskCreateRetriesWithOneIdempotencyID(t *testing.T) {
	t.Parallel()

	for _, retryableStatus := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		retryableStatus := retryableStatus
		t.Run(http.StatusText(retryableStatus), func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			wantBody, err := json.Marshal(taskCreateRequest{
				Title: "Retry task", Status: "todo", IdempotencyID: repeatedHex(0x66),
			})
			if err != nil {
				t.Fatal(err)
			}
			responseBody, err := json.Marshal(taskListItem{
				ID: "task-retry", Status: "todo", Title: "Retry task",
				CreatedAt: "2026-09-08T15:00:00.000000000Z", UpdatedAt: "2026-09-08T15:00:00.000000000Z",
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

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x66))
			client.RetryAttempts = 2
			saveTestCredential(t, client, credential)
			arguments := []string{"task", "create", "--workstream", "694", "--title", "Retry task"}
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("task create exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want 2", len(bodies))
			}
			for index, body := range bodies {
				if !bytes.Equal(body, wantBody) {
					t.Errorf("request %d body = %s, want %s", index+1, body, wantBody)
				}
			}
		})
	}
}
