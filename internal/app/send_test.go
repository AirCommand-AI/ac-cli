package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestSendDirectIDPostsAddressedMessageWithoutRosterFetch(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	messageBody := "  preserve this message exactly\n"
	idempotencyID := repeatedHex(0x44)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/agent/v1/workstreams/694/messages" {
			t.Errorf("request = %s %s, want addressed message POST", request.Method, request.URL.RequestURI())
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		contents, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(contents, &fields); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if want := []string{"body", "idempotencyId", "recipientId"}; !reflect.DeepEqual(keys, want) {
			t.Errorf("request fields = %v, want %v", keys, want)
		}
		var sent messageSendRequest
		if err := json.Unmarshal(contents, &sent); err != nil {
			t.Errorf("decode typed request: %v", err)
			return
		}
		if sent.RecipientID != "agm_recipient" {
			t.Errorf("recipient ID = %q, want agm_recipient", sent.RecipientID)
		}
		if sent.Body != messageBody {
			t.Error("message body was not preserved exactly")
		}
		if sent.IdempotencyID != idempotencyID {
			t.Error("message idempotency ID did not match generated value")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write(messageSuccessBody("agm_recipient", messageBody))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
	saveTestCredential(t, client, credential)
	exitCode := client.Run([]string{
		"send", "--workstream", "694", "--to", "agm_recipient", "--body", messageBody,
	})
	if exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("direct ID made %d requests, want one POST and no roster GET", requests)
	}
	if !strings.Contains(stdout.String(), `"recipientId":"agm_recipient"`) {
		t.Fatalf("send output omitted accepted recipient: %q", stdout.String())
	}
}

func TestSendResolvesActiveAgentNameWithExactThenEqualFoldMatching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		to        string
		agents    []rosterAgent
		recipient string
	}{
		{
			name: "exact match wins before folded ambiguity",
			to:   " pi ",
			agents: []rosterAgent{
				{AgentID: "agm_title", Name: " Pi ", Status: "active"},
				{AgentID: "agm_lower", Name: "pi", Status: "active"},
			},
			recipient: "agm_lower",
		},
		{
			name: "unique equal-fold match",
			to:   "PI",
			agents: []rosterAgent{
				{AgentID: "agm_pi", Name: " Pi ", Status: "active"},
				{AgentID: "agm_stopped", Name: "PI", Status: "stopped"},
				{AgentID: "agm_removed", Name: "pi", Status: "removed"},
			},
			recipient: "agm_pi",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credential := testCredential()
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				methods = append(methods, request.Method)
				switch request.Method {
				case http.MethodGet:
					_, _ = writer.Write(rosterResponseBody(test.agents...))
				case http.MethodPost:
					var sent messageSendRequest
					if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
						t.Errorf("decode send request: %v", err)
					}
					if sent.RecipientID != test.recipient {
						t.Errorf("resolved recipient = %q, want %q", sent.RecipientID, test.recipient)
					}
					writer.WriteHeader(http.StatusCreated)
					_, _ = writer.Write(messageSuccessBody(test.recipient, "hello"))
				default:
					t.Errorf("unexpected method %s", request.Method)
				}
			}))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x45))
			saveTestCredential(t, client, credential)
			if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", test.to, "--body", "hello"}); exitCode != 0 {
				t.Fatalf("send exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if want := []string{http.MethodGet, http.MethodPost}; !reflect.DeepEqual(methods, want) {
				t.Fatalf("request methods = %v, want %v", methods, want)
			}
		})
	}
}

func TestSendDoesNotResolveHumanCollaboratorNames(t *testing.T) {
	t.Parallel()

	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts++
			return
		}
		_, _ = writer.Write([]byte(`{"workstream":{"code":"694","collaborators":[{"accountId":"ac_human","name":"Operator","agents":[]}]},"tasks":[],"updates":[]}`))
	}))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", nil)
	saveTestCredential(t, client, testCredential())
	if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "Operator", "--body", "hello"}); exitCode == 0 {
		t.Fatal("send unexpectedly resolved a human collaborator name")
	}
	if posts != 0 || !strings.Contains(stderr.String(), "No active agent names are available") {
		t.Fatalf("human name resolution posts = %d, error = %q", posts, stderr.String())
	}
}

func TestSendNameResolutionFailsClosedWithActionableCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		to       string
		agents   []rosterAgent
		want     []string
		unwanted []string
	}{
		{
			name: "ambiguous folded name lists tied IDs",
			to:   "PI",
			agents: []rosterAgent{
				{AgentID: "agm_one", Name: "Pi", Status: "active"},
				{AgentID: "agm_two", Name: " pi ", Status: "active"},
			},
			want: []string{"ambiguous", "Pi (agm_one)", "pi (agm_two)", "--to <agentId>"},
		},
		{
			name: "missing name lists only active names",
			to:   "unknown",
			agents: []rosterAgent{
				{AgentID: "agm_z", Name: "Zulu", Status: "active"},
				{AgentID: "agm_a", Name: " Alpha ", Status: "active"},
				{AgentID: "agm_s", Name: "Stopped", Status: "stopped"},
			},
			want:     []string{`No active agent named "unknown"`, "Available active agent names: Alpha, Zulu"},
			unwanted: []string{"Stopped"},
		},
		{
			name: "canonically different Unicode is not normalized",
			to:   "e\u0301",
			agents: []rosterAgent{
				{AgentID: "agm_unicode", Name: "é", Status: "active"},
			},
			want: []string{"No active agent named", "é"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodPost {
					posts++
					t.Error("name resolution failure sent a message")
					return
				}
				_, _ = writer.Write(rosterResponseBody(test.agents...))
			}))
			defer server.Close()

			messageBody := "body_must_not_reach_errors"
			client, stdout, stderr := testApp(t, server.URL, "", nil)
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", test.to, "--body", messageBody}); exitCode == 0 {
				t.Fatal("send unexpectedly succeeded")
			}
			if posts != 0 || stdout.Len() != 0 {
				t.Fatalf("failed resolution posted %d messages and wrote stdout %q", posts, stdout.String())
			}
			for _, want := range test.want {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("error %q does not contain %q", stderr.String(), want)
				}
			}
			for _, unwanted := range append(test.unwanted, messageBody) {
				if strings.Contains(stderr.String(), unwanted) {
					t.Errorf("error contains forbidden value %q", unwanted)
				}
			}
		})
	}
}

func TestSendRetriesContractRetryableStatusesWithSameRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "request timeout", status: http.StatusRequestTimeout, body: `{"message":"Request timeout","code":"RequestTimeout"}`},
		{name: "internal server error", status: http.StatusInternalServerError, body: `{"message":"Internal Server Error","code":"InternalServerError"}`},
		{name: "authentication unavailable", status: http.StatusServiceUnavailable, body: `{"error":"Service Unavailable","code":"ServiceUnavailable"}`},
		{name: "message send unavailable", status: http.StatusServiceUnavailable, body: `{"message":"Message acceptance could not be confirmed; retry with the same idempotencyId","code":"MessageSendUnavailable"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var requests []messageSendRequest
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var sent messageSendRequest
				if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
					t.Errorf("decode send request: %v", err)
				}
				requests = append(requests, sent)
				if len(requests) < 3 {
					writer.WriteHeader(test.status)
					_, _ = writer.Write([]byte(test.body))
					return
				}
				writer.WriteHeader(http.StatusCreated)
				_, _ = writer.Write(messageSuccessBody("agm_recipient", "retry body"))
			}))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x46))
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "agm_recipient", "--body", "retry body"}); exitCode != 0 {
				t.Fatalf("send exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(requests) != 3 {
				t.Fatalf("request count = %d, want 3", len(requests))
			}
			for index := 1; index < len(requests); index++ {
				if !reflect.DeepEqual(requests[index], requests[0]) {
					t.Errorf("retry %d did not reuse the exact request", index+1)
				}
			}
		})
	}
}

func TestSendRetriesTransportFailureWithSameIdempotencyID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write(messageSuccessBody("agm_recipient", "transport body"))
	}))
	defer server.Close()

	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x47))
	saveTestCredential(t, client, testCredential())
	transport := &failOnceTransport{base: http.DefaultTransport}
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "agm_recipient", "--body", "transport body"}); exitCode != 0 {
		t.Fatalf("send exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if transport.calls != 2 || !bytes.Equal(transport.bodies[0], transport.bodies[1]) {
		t.Fatalf("transport retry calls = %d or request bodies changed", transport.calls)
	}
}

func TestSendDoesNotRetryKnownFinalStatusWhenResponseBodyReadFails(t *testing.T) {
	t.Parallel()

	client, _, stderr := testApp(t, "https://unused.invalid", "", deterministicRandom(0x48))
	saveTestCredential(t, client, testCredential())
	transport := &statusReadFailureTransport{status: http.StatusConflict}
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "agm_recipient", "--body", "secret_message_body"}); exitCode == 0 {
		t.Fatal("send unexpectedly succeeded")
	}
	if transport.calls != 1 {
		t.Fatalf("final HTTP status produced %d requests, want 1", transport.calls)
	}
	if !strings.Contains(stderr.String(), "HTTP 409") || strings.Contains(stderr.String(), "secret_message_body") {
		t.Fatalf("final status error was not safely mapped: %q", stderr.String())
	}
}

func TestSendDoesNotRetryFinalContractStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity,
	} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(`{"message":"body_must_not_be_echoed","code":"Final"}`))
			}))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x48))
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "agm_recipient", "--body", "secret_message_body"}); exitCode == 0 {
				t.Fatal("send unexpectedly succeeded")
			}
			if requests != 1 {
				t.Fatalf("HTTP %d produced %d requests, want 1", status, requests)
			}
			if strings.Contains(stderr.String(), "secret_message_body") || strings.Contains(stderr.String(), "body_must_not_be_echoed") {
				t.Fatal("message or response body reached the error output")
			}
		})
	}
}

func TestMessageStatusErrorMapsEveryDocumentedContractError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "invalid request body", status: 400, body: `{"message":"Invalid request body","code":"BadRequest"}`, want: "request as invalid"},
		{name: "recipient required", status: 400, body: `{"message":"recipientId is required","code":"BadRequest"}`, want: "recipient is required"},
		{name: "body required", status: 400, body: `{"message":"message body is required","code":"BadRequest"}`, want: "body is required"},
		{name: "idempotency missing", status: 400, body: `{"message":"missing idempotency key","code":"BadRequest"}`, want: "idempotency ID was missing"},
		{name: "idempotency long", status: 400, body: `{"message":"idempotency key is too long","code":"BadRequest"}`, want: "idempotency ID was too long"},
		{name: "unauthorized", status: 401, body: `{"error":"Unauthorized","code":"Unauthorized"}`, want: "no longer authorized"},
		{name: "not found", status: 404, body: `{"message":"Workstream not found","code":"NotFound"}`, want: "not found"},
		{name: "timeout", status: 408, body: `{"message":"Request timeout","code":"RequestTimeout"}`, want: "delivery is uncertain"},
		{name: "paused", status: 409, body: `{"message":"Workstream is paused","code":"WorkstreamPaused"}`, want: "paused"},
		{name: "recipient stopped", status: 409, body: `{"message":"Recipient is stopped","code":"RecipientStopped"}`, want: "is stopped"},
		{name: "recipient removed", status: 409, body: `{"message":"Recipient has been removed","code":"RecipientRemoved"}`, want: "has been removed"},
		{name: "recipient inactive", status: 409, body: `{"message":"Recipient is not active","code":"RecipientNotActive"}`, want: "is not active"},
		{name: "recipient ambiguous", status: 409, body: `{"message":"Recipient identity is ambiguous in this workstream","code":"RecipientAmbiguous"}`, want: "ID agm_recipient is ambiguous"},
		{name: "idempotency conflict", status: 409, body: `{"message":"Idempotency key was already used for a different message","code":"IdempotencyConflict"}`, want: "original message was not changed"},
		{name: "too large", status: 413, body: `{"message":"Message body exceeds the 32768-byte limit","code":"MessageTooLarge"}`, want: "32768-byte limit"},
		{name: "not participant", status: 422, body: `{"message":"Recipient is not a participant of this workstream","code":"RecipientNotParticipant"}`, want: "not a participant"},
		{name: "internal", status: 500, body: `{"message":"Internal Server Error","code":"InternalServerError"}`, want: "after retries"},
		{name: "auth unavailable", status: 503, body: `{"error":"Service Unavailable","code":"ServiceUnavailable"}`, want: "delivery is uncertain"},
		{name: "send unavailable", status: 503, body: `{"message":"Message acceptance could not be confirmed; retry with the same idempotencyId","code":"MessageSendUnavailable"}`, want: "delivery is uncertain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := messageStatusError(test.status, []byte(test.body), "694", "agm_recipient")
			visible, ok := err.(*publicError)
			if !ok {
				t.Fatalf("error type = %T, want *publicError", err)
			}
			if !strings.Contains(strings.ToLower(visible.message), strings.ToLower(test.want)) {
				t.Errorf("message %q does not contain %q", visible.message, test.want)
			}
			if strings.Contains(visible.message, test.body) {
				t.Error("raw error response was echoed")
			}
		})
	}
}

func TestSendExhaustionReportsUncertainDeliveryFor408And503(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusRequestTimeout, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			t.Parallel()

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(`{"message":"body_must_not_be_echoed","code":"MessageSendUnavailable"}`))
			}))
			defer server.Close()

			client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x49))
			saveTestCredential(t, client, testCredential())
			if exitCode := client.Run([]string{"send", "--workstream", "694", "--to", "ac_human", "--body", "secret_message_body"}); exitCode == 0 {
				t.Fatal("send unexpectedly succeeded")
			}
			if requests != 3 {
				t.Fatalf("retryable HTTP %d produced %d requests, want 3", status, requests)
			}
			if !strings.Contains(strings.ToLower(stderr.String()), "delivery is uncertain") {
				t.Errorf("error does not report uncertain delivery: %q", stderr.String())
			}
			if strings.Contains(stderr.String(), "secret_message_body") || strings.Contains(stderr.String(), "body_must_not_be_echoed") {
				t.Fatal("message or response body reached the error output")
			}
		})
	}
}

func TestExplicitHelpExitsZeroForTopLevelAndEveryCommand(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"--help"},
		{"-h"},
		{"exchange", "--help"},
		{"send", "--help"},
		{"update", "--help"},
		{"read", "--help"},
		{"listen", "--help"},
	} {
		arguments := arguments
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			t.Parallel()
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			client := &App{Stdout: stdout, Stderr: stderr}
			if exitCode := client.Run(arguments); exitCode != 0 {
				t.Fatalf("help exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), "Usage: ac-cli ") {
				t.Fatalf("help output = %q", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("help stderr = %q, want empty", stderr.String())
			}
		})
	}
}

type statusReadFailureTransport struct {
	calls  int
	status int
}

func (transport *statusReadFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls++
	return &http.Response{
		StatusCode: transport.status,
		Header:     make(http.Header),
		Body:       readFailureBody{},
	}, nil
}

type readFailureBody struct{}

func (readFailureBody) Read([]byte) (int, error) {
	return 0, errors.New("injected response read failure")
}
func (readFailureBody) Close() error { return nil }

// rosterResponseBody mirrors the agent API's workstream detail response, which
// nests the roster under "workstream" alongside "tasks" and "updates". It is
// deliberately built from a literal map rather than by marshalling
// workstreamRoster: marshalling the same struct the decoder reads makes the test
// a self-consistent round trip that cannot detect a mismatch with the real API,
// which is exactly how a flat-roster decoder shipped while every test passed.
func rosterResponseBody(agents ...rosterAgent) []byte {
	rosterAgents := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		rosterAgents = append(rosterAgents, map[string]any{
			"agentId": agent.AgentID,
			"name":    agent.Name,
			"status":  agent.Status,
		})
	}
	contents, err := json.Marshal(map[string]any{
		"workstream": map[string]any{
			"code": "694",
			"collaborators": []map[string]any{
				{"accountId": "ac_operator", "name": "Operator", "agents": rosterAgents},
			},
		},
		"tasks":   []any{},
		"updates": []any{},
	})
	if err != nil {
		panic(err)
	}
	return contents
}

func messageSuccessBody(recipientID string, body string) []byte {
	contents, err := json.Marshal(map[string]any{
		"workstreamCode":  "694",
		"id":              "message-1",
		"senderId":        "agent-7",
		"senderNature":    "agent",
		"recipientId":     recipientID,
		"recipientNature": "agent",
		"body":            body,
		"createdAt":       "2026-09-04T12:34:56.123456789Z",
	})
	if err != nil {
		panic(err)
	}
	return contents
}

func saveTestCredential(t *testing.T, client *App, credential credentials.Credential) {
	t.Helper()
	if err := client.Store.Save(credential); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
}

// TestDecodeWorkstreamRosterAcceptsProductionResponse guards against the roster
// decoder drifting from the real agent API. The body below is a verbatim
// production response captured from
// GET /agent/v1/workstreams/{code} on 2026-09-04, trimmed only of unrelated
// fields. It is deliberately a raw string rather than anything marshalled from
// this package's own types: the original defect was a decoder expecting
// "collaborators" at the top level while the API nests it under "workstream",
// and every existing test passed because the fixtures were generated from the
// same wrong struct. Name resolution silently failed for every agent.
func TestDecodeWorkstreamRosterAcceptsProductionResponse(t *testing.T) {
	t.Parallel()

	const production = `{"workstream":{"code":"493","name":"Demo 1","description":"","status":"active","collaborators":[{"accountId":"ac_9flvj6ff80hq","name":"rsingh@arrsingh.com","agents":[{"agentId":"agm_caf16d31b0ed1b0bac73429f329224c8","name":"Pi","status":"active","socketAddress":"ac:agm_caf16d31b0ed1b0bac73429f329224c8","generation":1,"createdAt":"2026-09-04T20:54:11.000000000Z","updatedAt":"2026-09-04T20:54:11.000000000Z"},{"agentId":"agm_77330edc1aa628398f6626a529fc66d1","name":"Claude","status":"active","socketAddress":"ac:agm_77330edc1aa628398f6626a529fc66d1","generation":1,"createdAt":"2026-09-04T20:54:20.000000000Z","updatedAt":"2026-09-04T20:54:20.000000000Z"}]}],"createdAt":"2026-09-04T20:53:00.000000000Z","updatedAt":"2026-09-04T20:54:20.000000000Z"},"tasks":[],"updates":[]}`

	roster, err := decodeWorkstreamRoster([]byte(production))
	if err != nil {
		t.Fatalf("decode production roster: %v", err)
	}

	found := map[string]string{}
	for _, collaborator := range roster.Collaborators {
		for _, agent := range collaborator.Agents {
			found[agent.Name] = agent.AgentID
		}
	}
	if len(found) != 2 {
		t.Fatalf("decoded %d agents from the production roster, want 2: %+v", len(found), found)
	}
	if got := found["Pi"]; got != "agm_caf16d31b0ed1b0bac73429f329224c8" {
		t.Errorf("agent \"Pi\" = %q", got)
	}
	if got := found["Claude"]; got != "agm_77330edc1aa628398f6626a529fc66d1" {
		t.Errorf("agent \"Claude\" = %q", got)
	}
}
