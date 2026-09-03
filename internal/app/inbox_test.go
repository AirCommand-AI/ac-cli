package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestInboxUnreadReturnsOneJSONPageWithoutAcknowledging(t *testing.T) {
	t.Parallel()

	page := `{"messages":[{"workstreamCode":"694","id":"0123456789abcdef","senderId":"agm_sender","senderNature":"agent","recipientId":"agent-7","recipientNature":"agent","body":"body_for_agent_context_only","createdAt":"2026-09-04T12:34:56.123456789Z"}],"nextCursor":"2026-09-04T12:34:56.123456789Z#0123456789abcdef"}`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet {
			t.Errorf("list method = %s, want GET; listing must never acknowledge", request.Method)
		}
		if request.URL.Path != "/agent/v1/workstreams/694/messages/unread" {
			t.Errorf("list path = %q, want unread route", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "1" {
			t.Errorf("limit = %q, want 1", request.URL.Query().Get("limit"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(page))
	}))
	defer server.Close()

	home := t.TempDir()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	client := &App{
		BaseURL:       server.URL,
		HTTPClient:    http.DefaultClient,
		Store:         credentials.NewStore(home),
		Stdout:        stdout,
		Stderr:        stderr,
		RetryAttempts: 3,
		RetryDelay:    func(int) {},
	}
	saveTestCredential(t, client, testCredential())
	if exitCode := client.Run([]string{"inbox", "--workstream", "694", "--limit", "1"}); exitCode != 0 {
		t.Fatalf("inbox exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("inbox made %d requests, want exactly one page request", requests)
	}
	if got, want := stdout.String(), page+"\n"; got != want {
		t.Fatalf("inbox JSON output did not preserve the server page")
	}
	if !strings.Contains(stdout.String(), `"nextCursor"`) {
		t.Fatal("inbox output did not surface the continuation cursor")
	}
	spoolPath := filepath.Join(home, "agents", testCredential().AgentID, "spool.jsonl")
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inbox created or accessed a spool: %v", err)
	}
}

func TestInboxAcceptsAContractValidPageLargerThanOrdinaryResponseLimit(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("\n", 32*1024)
	messages := make([]map[string]string, 65)
	for index := range messages {
		messages[index] = map[string]string{
			"workstreamCode":  "694",
			"id":              "0123456789abcdef",
			"senderId":        "agm_sender",
			"senderNature":    "agent",
			"recipientId":     "agent-7",
			"recipientNature": "agent",
			"body":            body,
			"createdAt":       "2026-09-04T12:34:56.123456789Z",
		}
	}
	page, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		t.Fatalf("marshal large valid page: %v", err)
	}
	if len(page) <= maxResponseBytes {
		t.Fatalf("test page size = %d, want larger than ordinary response limit", len(page))
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(page)
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, testCredential())
	if exitCode := client.Run([]string{"inbox", "--workstream", "694", "--limit", "65"}); exitCode != 0 {
		t.Fatalf("large inbox page exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.Len() != len(page)+1 {
		t.Fatalf("large inbox output size = %d, want %d", stdout.Len(), len(page)+1)
	}
}

func TestInboxAllURLencodesOpaqueCursorAndDoesNotAutoPage(t *testing.T) {
	t.Parallel()

	cursor := "2026-09-04T12:34:56.123456789Z#0123456789abcdef"
	nextCursor := "2026-09-04T12:35:00.000000000Z#fedcba9876543210"
	page := `{"messages":[],"nextCursor":"` + nextCursor + `"}`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/agent/v1/workstreams/694/messages/all" {
			t.Errorf("request = %s %s, want all-message GET", request.Method, request.URL.Path)
		}
		if got := request.URL.Query().Get("cursor"); got != cursor {
			t.Errorf("decoded cursor = %q, want original opaque cursor", got)
		}
		if got := request.URL.Query().Get("limit"); got != "100" {
			t.Errorf("limit = %q, want 100", got)
		}
		if !strings.Contains(request.RequestURI, "%23") || request.URL.Fragment != "" {
			t.Errorf("cursor was not safely URL-encoded: %q", request.RequestURI)
		}
		_, _ = writer.Write([]byte(page))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, testCredential())
	if exitCode := client.Run([]string{"inbox", "--workstream", "694", "--all", "--limit", "100", "--cursor", cursor}); exitCode != 0 {
		t.Fatalf("inbox --all exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("inbox --all made %d requests despite receiving nextCursor", requests)
	}
	if got, want := stdout.String(), page+"\n"; got != want {
		t.Fatalf("all-message output = %q, want %q", got, want)
	}
}

func TestInboxRejectsInvalidLimitsBeforeReadingCredentialsOrCallingServer(t *testing.T) {
	t.Parallel()

	for _, limit := range []string{"0", "101", "-1", "+1", "1.5", " 1 ", "999999999999999999999999999999"} {
		limit := limit
		t.Run(limit, func(t *testing.T) {
			t.Parallel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			client := &App{BaseURL: server.URL, Stdout: stdout, Stderr: stderr}
			if exitCode := client.Run([]string{"inbox", "--workstream", "694", "--limit", limit}); exitCode == 0 {
				t.Fatal("invalid inbox limit unexpectedly succeeded")
			}
			if requests != 0 {
				t.Fatalf("invalid limit made %d requests", requests)
			}
			if !strings.Contains(stderr.String(), "integer from 1 to 100") {
				t.Fatalf("invalid-limit error = %q", stderr.String())
			}
		})
	}
}

func TestAckPostsExactMessagePathWithoutRequestBody(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	response := `{"messageId":"0123456789abcdef","acknowledged":true}`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/agent/v1/workstreams/694/messages/0123456789abcdef/acknowledge" {
			t.Errorf("request = %s %s, want acknowledge POST", request.Method, request.URL.RequestURI())
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		contents, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read acknowledge body: %v", err)
		}
		if len(contents) != 0 || request.Header.Get("Content-Type") != "" {
			t.Error("acknowledgement unexpectedly sent a request body")
		}
		_, _ = writer.Write([]byte(response))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, credential)
	if exitCode := client.Run([]string{"ack", "--workstream", "694", "--message", "0123456789abcdef"}); exitCode != 0 {
		t.Fatalf("ack exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("ack request count = %d, want 1", requests)
	}
	if got, want := stdout.String(), response+"\n"; got != want {
		t.Fatalf("ack output = %q, want %q", got, want)
	}
}

func TestAckRejectsInvalidMessageIDsWithoutCallingServer(t *testing.T) {
	t.Parallel()

	for _, messageID := range []string{"short", "0123456789ABCDEf", "0123456789abcdeg", "0123456789abcdef0", "../../credentials"} {
		messageID := messageID
		t.Run(messageID, func(t *testing.T) {
			t.Parallel()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			client := &App{BaseURL: server.URL, Stdout: stdout, Stderr: stderr}
			if exitCode := client.Run([]string{"ack", "--workstream", "694", "--message", messageID}); exitCode == 0 {
				t.Fatal("invalid message ID unexpectedly succeeded")
			}
			if requests != 0 {
				t.Fatalf("invalid message ID made %d requests", requests)
			}
			if !strings.Contains(stderr.String(), "16 lowercase hexadecimal") {
				t.Fatalf("invalid-ID error = %q", stderr.String())
			}
		})
	}
}

func TestInboxAndAckRetryContractStatusesWithTheSameRequest(t *testing.T) {
	t.Parallel()

	operations := []struct {
		name        string
		arguments   []string
		method      string
		path        string
		successBody string
	}{
		{
			name:        "inbox",
			arguments:   []string{"inbox", "--workstream", "694", "--limit", "2"},
			method:      http.MethodGet,
			path:        "/agent/v1/workstreams/694/messages/unread?limit=2",
			successBody: `{"messages":[]}`,
		},
		{
			name:        "ack",
			arguments:   []string{"ack", "--workstream", "694", "--message", "0123456789abcdef"},
			method:      http.MethodPost,
			path:        "/agent/v1/workstreams/694/messages/0123456789abcdef/acknowledge",
			successBody: `{"messageId":"0123456789abcdef","acknowledged":true}`,
		},
	}
	statuses := []struct {
		name   string
		status int
		body   string
	}{
		{name: "408", status: http.StatusRequestTimeout, body: `{"message":"Request timeout","code":"RequestTimeout"}`},
		{name: "500", status: http.StatusInternalServerError, body: `{"message":"Internal Server Error","code":"InternalServerError"}`},
		{name: "503", status: http.StatusServiceUnavailable, body: `{"message":"temporarily unavailable","code":"MessageReadUnavailable"}`},
	}
	for _, operation := range operations {
		operation := operation
		for _, status := range statuses {
			status := status
			t.Run(operation.name+"_"+status.name, func(t *testing.T) {
				t.Parallel()

				var requests []string
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					contents, err := io.ReadAll(request.Body)
					if err != nil {
						t.Errorf("read request body: %v", err)
					}
					requests = append(requests, request.Method+" "+request.URL.RequestURI()+" "+string(contents))
					if len(requests) < 3 {
						writer.WriteHeader(status.status)
						_, _ = writer.Write([]byte(status.body))
						return
					}
					_, _ = writer.Write([]byte(operation.successBody))
				}))
				defer server.Close()

				client, _, stderr := testApp(t, server.URL, "", nil)
				saveTestCredential(t, client, testCredential())
				if exitCode := client.Run(operation.arguments); exitCode != 0 {
					t.Fatalf("command exit code = %d, stderr = %q", exitCode, stderr.String())
				}
				if len(requests) != 3 {
					t.Fatalf("request count = %d, want 3", len(requests))
				}
				for _, request := range requests {
					if request != operation.method+" "+operation.path+" " {
						t.Errorf("retry changed request: %q", request)
					}
				}
			})
		}
	}
}

func TestAckRetriesTransportFailure(t *testing.T) {
	t.Parallel()

	response := `{"messageId":"0123456789abcdef","acknowledged":true}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(response))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, testCredential())
	transport := &failFirstEmptyRequestTransport{base: http.DefaultTransport}
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"ack", "--workstream", "694", "--message", "0123456789abcdef"}); exitCode != 0 {
		t.Fatalf("ack exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want 2", transport.calls)
	}
	if got, want := stdout.String(), response+"\n"; got != want {
		t.Fatalf("ack output = %q, want %q", got, want)
	}
}

func TestInboxAndAckDoNotRetryFinalStatuses(t *testing.T) {
	t.Parallel()

	operations := []struct {
		name      string
		arguments []string
	}{
		{name: "inbox", arguments: []string{"inbox", "--workstream", "694"}},
		{name: "ack", arguments: []string{"ack", "--workstream", "694", "--message", "0123456789abcdef"}},
	}
	for _, operation := range operations {
		operation := operation
		for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
			status := status
			t.Run(operation.name+"_"+http.StatusText(status), func(t *testing.T) {
				t.Parallel()
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					requests++
					writer.WriteHeader(status)
					_, _ = writer.Write([]byte(`{"message":"body_must_not_be_echoed","code":"Final"}`))
				}))
				defer server.Close()

				client, _, stderr := testApp(t, server.URL, "", nil)
				saveTestCredential(t, client, testCredential())
				if exitCode := client.Run(operation.arguments); exitCode == 0 {
					t.Fatal("final status unexpectedly succeeded")
				}
				if requests != 1 {
					t.Fatalf("final status made %d requests, want 1", requests)
				}
				if strings.Contains(stderr.String(), "body_must_not_be_echoed") {
					t.Fatal("response body reached error output")
				}
			})
		}
	}
}

func TestMessageReadStatusErrorMapsEveryDocumentedError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		body      string
		operation messageReadOperation
		want      string
	}{
		{name: "invalid pagination", status: 400, body: `{"message":"limit must be an integer from 1 to 100","code":"InvalidMessagePagination","body":"secret"}`, operation: messageListOperation, want: "integer from 1 to 100"},
		{name: "invalid cursor", status: 400, body: `{"message":"Invalid message cursor","code":"InvalidMessageCursor","body":"secret"}`, operation: messageListOperation, want: "same inbox mode"},
		{name: "invalid message ID", status: 400, body: `{"message":"Invalid message ID","code":"InvalidMessageID","body":"secret"}`, operation: messageAcknowledgeOperation, want: "16 lowercase hexadecimal"},
		{name: "unauthorized", status: 401, body: `{"error":"Unauthorized","code":"Unauthorized","body":"secret"}`, operation: messageListOperation, want: "no longer authorized"},
		{name: "workstream not found", status: 404, body: `{"message":"Workstream not found","code":"NotFound","body":"secret"}`, operation: messageListOperation, want: "Workstream 694 was not found"},
		{name: "message not found", status: 404, body: `{"message":"Message not found","code":"MessageNotFound","body":"secret"}`, operation: messageAcknowledgeOperation, want: "not found or does not belong"},
		{name: "list timeout", status: 408, body: `{"message":"Request timeout","code":"RequestTimeout","body":"secret"}`, operation: messageListOperation, want: "no page was returned"},
		{name: "ack timeout", status: 408, body: `{"message":"Request timeout","code":"RequestTimeout","body":"secret"}`, operation: messageAcknowledgeOperation, want: "safe to repeat"},
		{name: "list internal", status: 500, body: `{"message":"Internal Server Error","code":"InternalServerError","body":"secret"}`, operation: messageListOperation, want: "listing after retries"},
		{name: "ack internal", status: 500, body: `{"message":"Internal Server Error","code":"InternalServerError","body":"secret"}`, operation: messageAcknowledgeOperation, want: "safe to repeat"},
		{name: "service unavailable", status: 503, body: `{"error":"Service Unavailable","code":"ServiceUnavailable","body":"secret"}`, operation: messageListOperation, want: "authentication remained unavailable"},
		{name: "read unavailable", status: 503, body: `{"message":"Message read could not be completed; retry the request","code":"MessageReadUnavailable","body":"secret"}`, operation: messageListOperation, want: "no page was returned"},
		{name: "ack unavailable", status: 503, body: `{"message":"Message acknowledgement could not be confirmed; retry the request","code":"MessageAcknowledgeUnavailable","body":"secret"}`, operation: messageAcknowledgeOperation, want: "safe to repeat"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := messageReadStatusError(test.status, []byte(test.body), test.operation, "694", "0123456789abcdef")
			visible, ok := err.(*publicError)
			if !ok {
				t.Fatalf("error type = %T, want *publicError", err)
			}
			if !strings.Contains(visible.message, test.want) {
				t.Errorf("mapped error %q does not contain %q", visible.message, test.want)
			}
			if strings.Contains(visible.message, "secret") || strings.Contains(visible.message, test.body) {
				t.Error("message body or raw response reached mapped error")
			}
		})
	}
}

func TestInboxAndAckHelpExitZero(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"inbox", "--help"}, {"ack", "--help"}} {
		arguments := arguments
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			t.Parallel()
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			client := &App{Stdout: stdout, Stderr: stderr}
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("help exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), "Usage: ac-cli ") || stderr.Len() != 0 {
				t.Fatalf("help stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

type failFirstEmptyRequestTransport struct {
	base  http.RoundTripper
	calls int
}

func (transport *failFirstEmptyRequestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	if transport.calls == 1 {
		return nil, errors.New("injected transport failure")
	}
	return transport.base.RoundTrip(request)
}

func TestValidateMessageLimit(t *testing.T) {
	t.Parallel()

	for _, limit := range []string{"", "1", "01", "50", "100"} {
		if err := validateMessageLimit(limit); err != nil {
			t.Errorf("validateMessageLimit(%q) = %v", limit, err)
		}
	}
}

func TestValidMessageID(t *testing.T) {
	t.Parallel()

	if !validMessageID("0123456789abcdef") {
		t.Fatal("valid message ID was rejected")
	}
	for _, messageID := range []string{"", "0123456789abcde", "0123456789abcdef0", "0123456789ABCDEf", "0123456789abcdeg"} {
		if validMessageID(messageID) {
			t.Errorf("invalid message ID %q was accepted", messageID)
		}
	}
}

func TestInboxRequestShapeIsStableAcrossRetry(t *testing.T) {
	t.Parallel()

	want := []string{
		"GET /agent/v1/workstreams/694/messages/all?cursor=cursor%23value&limit=10",
		"GET /agent/v1/workstreams/694/messages/all?cursor=cursor%23value&limit=10",
	}
	transport := &recordingRetryTransport{responses: []transportResponse{
		{err: errors.New("injected transport failure")},
		{status: http.StatusOK, body: `{"messages":[]}`},
	}}
	client, _, stderr := testApp(t, "https://unused.invalid", "", nil)
	saveTestCredential(t, client, testCredential())
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"inbox", "--workstream", "694", "--all", "--limit", "10", "--cursor", "cursor#value"}); exitCode != 0 {
		t.Fatalf("inbox exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !reflect.DeepEqual(transport.requests, want) {
		t.Fatalf("requests = %v, want %v", transport.requests, want)
	}
}

type transportResponse struct {
	status int
	body   string
	err    error
}

type recordingRetryTransport struct {
	responses []transportResponse
	requests  []string
}

func (transport *recordingRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.requests = append(transport.requests, request.Method+" "+request.URL.RequestURI())
	response := transport.responses[0]
	transport.responses = transport.responses[1:]
	if response.err != nil {
		return nil, response.err
	}
	return &http.Response{
		StatusCode: response.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response.body)),
	}, nil
}
