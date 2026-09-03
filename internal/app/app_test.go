package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func TestExchangeIntegrationUsesStdinAndReusesRequestOnTransportRetry(t *testing.T) {
	t.Parallel()

	ticket := "setup_ticket_from_stdin"
	apiToken := "api_" + repeatedHex(0x11)
	socketKey := "sock_" + repeatedHex(0x22)
	idempotencyID := repeatedHex(0x33)
	wantBody, err := json.Marshal(exchangeRequest{
		TicketSecret:  ticket,
		APIToken:      apiToken,
		SocketKey:     socketKey,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		t.Fatalf("marshal expected request: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.RequestURI() != "/ajax/enrollment/exchange" {
			t.Errorf("request URI = %q, want /ajax/enrollment/exchange", request.URL.RequestURI())
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		if !bytes.Equal(body, wantBody) {
			t.Errorf("body = %s, want %s", body, wantBody)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"agentId":"agent-7","agentName":"Builder","socketAddress":"ac:agent-7","workstreamId":"workstream-694","workstreamCode":"694","generation":1,"consumedAt":"2026-09-01T19:05:34Z"}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, ticket+"\n", deterministicRandom(0x11, 0x22, 0x33))
	transport := &failOnceTransport{base: http.DefaultTransport}
	client.HTTPClient = &http.Client{Transport: transport}
	if exitCode := client.Run([]string{"exchange"}); exitCode != 0 {
		t.Fatalf("exchange exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if transport.calls != 2 {
		t.Fatalf("exchange transport attempts = %d, want 2", transport.calls)
	}
	for index, body := range transport.bodies {
		if !bytes.Equal(body, wantBody) {
			t.Errorf("transport request %d body = %s, want %s", index+1, body, wantBody)
		}
	}

	output := stdout.String()
	for name, secret := range map[string]string{"ticket": ticket, "API token": apiToken, "socket key": socketKey} {
		if strings.Contains(output, secret) || strings.Contains(stderr.String(), secret) {
			t.Errorf("%s reached command output", name)
		}
	}
	if !strings.HasPrefix(output, "Agent ID: agent-7\nUse for send/update/read/inbox/ack/listen: --agent agent-7\n") {
		t.Errorf("exchange output does not prominently identify the agent: %q", output)
	}
	for _, metadata := range []string{"Builder", "agent-7", "694", "ac:agent-7"} {
		if !strings.Contains(output, metadata) {
			t.Errorf("exchange output %q does not contain %q", output, metadata)
		}
	}

	credentialPath := client.Store.Path("agent-7")
	if wantSuffix := filepath.Join(".aircommand", "agents", "agent-7", "credentials.json"); !strings.HasSuffix(credentialPath, wantSuffix) {
		t.Fatalf("exchange credential path = %q, want suffix %q", credentialPath, wantSuffix)
	}
	if info, err := os.Stat(credentialPath); err != nil {
		t.Fatalf("stat exchanged credential: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("exchanged credential mode = %o, want 600", got)
	}

	stored, err := client.Store.FindByWorkstream("694")
	if err != nil {
		t.Fatalf("FindByWorkstream: %v", err)
	}
	wantCredential := credentials.Credential{
		APIToken:       apiToken,
		SocketKey:      socketKey,
		WorkstreamCode: "694",
		AgentID:        "agent-7",
		SocketAddress:  "ac:agent-7",
	}
	if stored != wantCredential {
		t.Fatalf("stored credential = %#v, want %#v", stored, wantCredential)
	}
}

func TestUpdateIntegrationPreservesBroadcastBehavior(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	idempotencyID := repeatedHex(0x44)
	wantBody, err := json.Marshal(updateRequest{Body: "starting on the parser", IdempotencyID: idempotencyID})
	if err != nil {
		t.Fatalf("marshal expected request: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.RequestURI() != "/agent/v1/workstreams/694/updates" {
			t.Errorf("request URI = %q, want /agent/v1/workstreams/694/updates", request.URL.RequestURI())
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		if !bytes.Equal(body, wantBody) {
			t.Errorf("body = %s, want %s", body, wantBody)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"updateId":"update-1"}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x44))
	if err := client.Store.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	arguments := []string{"update", "--workstream", "694", "--body", "starting on the parser"}
	if exitCode := client.Run(arguments); exitCode != 0 {
		t.Fatalf("update exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if got, want := stdout.String(), "{\"updateId\":\"update-1\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestReadIntegration(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if request.URL.RequestURI() != "/agent/v1/workstreams/694" {
			t.Errorf("request URI = %q, want /agent/v1/workstreams/694", request.URL.RequestURI())
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+credential.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		if len(body) != 0 {
			t.Errorf("GET body = %q, want empty", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":"694","updates":[{"body":"starting on the parser"}]}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	if err := client.Store.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if exitCode := client.Run([]string{"read", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("read exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "{\"code\":\"694\",\"updates\":[{\"body\":\"starting on the parser\"}]}\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAgentSelectorDisambiguatesTwoAgentsInOneWorkstream(t *testing.T) {
	t.Parallel()

	claude := testCredential()
	claude.AgentID = "agent-claude"
	claude.APIToken = "api_" + repeatedHex(0xc1)
	claude.SocketKey = "sock_" + repeatedHex(0xc2)
	claude.SocketAddress = "ac:agent-claude"
	pi := testCredential()
	pi.AgentID = "agent-pi"
	pi.APIToken = "api_" + repeatedHex(0xd1)
	pi.SocketKey = "sock_" + repeatedHex(0xd2)
	pi.SocketAddress = "ac:agent-pi"

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got, want := request.Header.Get("Authorization"), "Bearer "+pi.APIToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x61))
	for _, credential := range []credentials.Credential{claude, pi} {
		if err := client.Store.Save(credential); err != nil {
			t.Fatalf("Save(%s): %v", credential.AgentID, err)
		}
	}
	if err := os.WriteFile(client.Store.Path(claude.AgentID), []byte("unselected credential must not be read"), 0o600); err != nil {
		t.Fatalf("invalidate unselected credential: %v", err)
	}

	if exitCode := client.Run([]string{"update", "--workstream", "694", "--agent", "agent-pi", "--body", "hello"}); exitCode != 0 {
		t.Fatalf("selected update exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := client.Run([]string{"read", "--workstream", "694", "--agent", "agent-pi"}); exitCode != 0 {
		t.Fatalf("selected read exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if requests != 2 {
		t.Fatalf("selected commands sent %d requests, want 2", requests)
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := client.Run([]string{"read", "--workstream", "694"}); exitCode == 0 {
		t.Fatal("ambiguous read unexpectedly succeeded")
	}
	if requests != 2 {
		t.Fatalf("ambiguous read sent a request; request count = %d, want 2", requests)
	}
	message := stderr.String()
	for _, expected := range []string{"agent-claude", "agent-pi", "--agent <agentId>"} {
		if !strings.Contains(message, expected) {
			t.Errorf("ambiguous error %q does not contain %q", message, expected)
		}
	}
}

func TestOldLayoutStopsExchangeBeforeTicketUseWithReenrollmentGuidance(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aircommand", "spool"), 0o700); err != nil {
		t.Fatalf("create old spool: %v", err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	client := &App{
		BaseURL:     server.URL,
		HTTPClient:  http.DefaultClient,
		Store:       credentials.NewStore(home),
		ListenStore: listenstore.NewStore(home),
		Stdin:       strings.NewReader("setup_ticket"),
		Stdout:      stdout,
		Stderr:      stderr,
	}
	if exitCode := client.Run([]string{"exchange"}); exitCode == 0 {
		t.Fatal("exchange with old storage unexpectedly succeeded")
	}
	if called {
		t.Fatal("exchange consumed the ticket before rejecting old storage")
	}
	if stdout.Len() != 0 {
		t.Fatalf("exchange stdout = %q, want empty", stdout.String())
	}
	message := stderr.String()
	for _, expected := range []string{"old AirCommand storage layout", "will not be read or migrated", "re-enroll"} {
		if !strings.Contains(message, expected) {
			t.Errorf("legacy storage error %q does not contain %q", message, expected)
		}
	}
}

func TestExchangeRejectsTicketArgumentWithoutSendingIt(t *testing.T) {
	t.Parallel()

	ticket := "setup_ticket_must_not_be_in_argv"
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "ticket_from_stdin", deterministicRandom(0x11, 0x22, 0x33))
	if exitCode := client.Run([]string{"exchange", ticket}); exitCode == 0 {
		t.Fatal("exchange with a ticket argument succeeded")
	}
	if called {
		t.Fatal("exchange sent an HTTP request after receiving an argument")
	}
	if strings.Contains(stdout.String(), ticket) || strings.Contains(stderr.String(), ticket) {
		t.Fatal("ticket argument reached stdout or stderr")
	}
}

func TestExchangeDoesNotReadTicketFromEnvironment(t *testing.T) {
	ticket := "setup_ticket_from_environment"
	t.Setenv("AIRCOMMAND_TICKET", ticket)
	t.Setenv("AC_TICKET", ticket)
	t.Setenv("TICKET_SECRET", ticket)

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x11, 0x22, 0x33))
	if exitCode := client.Run([]string{"exchange"}); exitCode == 0 {
		t.Fatal("exchange without a stdin ticket succeeded")
	}
	if called {
		t.Fatal("exchange used a ticket from the environment")
	}
	if strings.Contains(stdout.String(), ticket) || strings.Contains(stderr.String(), ticket) {
		t.Fatal("ticket from the environment reached stdout or stderr")
	}
}

func TestFailureResponseCannotLeakSecrets(t *testing.T) {
	t.Parallel()

	ticket := "setup_failure_ticket"
	apiToken := "api_" + repeatedHex(0x51)
	socketKey := "sock_" + repeatedHex(0x52)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(writer, `{"ticket":%q,"apiToken":%q,"socketKey":%q}`, ticket, apiToken, socketKey)
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, ticket, deterministicRandom(0x51, 0x52, 0x53))
	if exitCode := client.Run([]string{"exchange"}); exitCode == 0 {
		t.Fatal("exchange unexpectedly succeeded")
	}
	combined := stdout.String() + stderr.String()
	for name, secret := range map[string]string{"ticket": ticket, "API token": apiToken, "socket key": socketKey} {
		if strings.Contains(combined, secret) {
			t.Errorf("%s reached stdout or stderr: %q", name, combined)
		}
	}
}

func TestSuccessfulAgentResponseRedactsStoredSecrets(t *testing.T) {
	t.Parallel()

	credential := testCredential()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"apiToken":%q,"socketKey":%q}`, credential.APIToken, credential.SocketKey)
	}))
	defer server.Close()

	client, stdout, stderr := testApp(t, server.URL, "", nil)
	if err := client.Store.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if exitCode := client.Run([]string{"read", "--workstream", "694"}); exitCode != 0 {
		t.Fatalf("read exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, credential.APIToken) || strings.Contains(combined, credential.SocketKey) {
		t.Fatalf("stored secret reached stdout or stderr: %q", combined)
	}
}

func TestWorkstreamStatusMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		code   string
		write  bool
		want   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: "You were stopped or removed from workstream 694."},
		{name: "not found", status: http.StatusNotFound, want: "Workstream 694 was not found or is not available to this agent."},
		{name: "paused", status: http.StatusConflict, code: "WorkstreamPaused", write: true, want: "Workstream 694 is paused; write rejected."},
		{name: "paused read is generic", status: http.StatusConflict, code: "WorkstreamPaused", want: "AirCommand request failed (HTTP 409)."},
		{name: "other conflict is generic", status: http.StatusConflict, code: "Other", write: true, want: "AirCommand request failed (HTTP 409)."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := workstreamStatusError(test.status, test.code, "694", test.write)
			visible, ok := err.(*publicError)
			if !ok {
				t.Fatalf("error type = %T, want *publicError", err)
			}
			if visible.message != test.want {
				t.Fatalf("message = %q, want %q", visible.message, test.want)
			}
		})
	}
}

func TestResponseCodeUsesTopLevelContract(t *testing.T) {
	t.Parallel()

	if got := responseCode([]byte(`{"code":"WorkstreamPaused"}`)); got != "WorkstreamPaused" {
		t.Fatalf("top-level code = %q", got)
	}
	if got := responseCode([]byte(`{"error":{"code":"WorkstreamPaused"}}`)); got != "" {
		t.Fatalf("speculative nested code = %q, want empty", got)
	}
}

func TestExchangeResponseRequiresFieldsAndAllowsAdditions(t *testing.T) {
	t.Parallel()

	valid := []byte(`{"agentId":"agent-7","agentName":"Builder","socketAddress":"ac:agent-7","workstreamId":"workstream-694","workstreamCode":"694","generation":1,"consumedAt":"2026-09-01T19:05:34Z"}`)
	response, err := decodeExchangeResponse(valid)
	if err != nil {
		t.Fatalf("decode exact response: %v", err)
	}
	if response.AgentID != "agent-7" || response.WorkstreamID != "workstream-694" || response.Generation == nil || *response.Generation != 1 {
		t.Fatalf("decoded response = %#v", response)
	}

	withUnknownField := []byte(`{"agentId":"agent-7","agentName":"Builder","socketAddress":"ac:agent-7","workstreamId":"workstream-694","workstreamCode":"694","generation":1,"consumedAt":"2026-09-01T19:05:34Z","other":"additive"}`)
	if _, err := decodeExchangeResponse(withUnknownField); err != nil {
		t.Fatalf("decode response with additive field: %v", err)
	}

	for name, body := range map[string][]byte{
		"nested":        []byte(`{"agent":{"id":"agent-7"}}`),
		"missing field": []byte(`{"agentId":"agent-7","agentName":"Builder","socketAddress":"ac:agent-7","workstreamCode":"694","generation":1,"consumedAt":"2026-09-01T19:05:34Z"}`),
		"trailing data": append(append([]byte(nil), valid...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeExchangeResponse(body); err == nil {
				t.Fatal("unexpected response shape decoded successfully")
			}
		})
	}
}

func TestHTTPStatusIsNotRetried(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"code":"Unavailable"}`))
	}))
	defer server.Close()

	client, _, _ := testApp(t, server.URL, "setup_ticket", deterministicRandom(0x71, 0x72, 0x73))
	client.RetryDelay = func(int) { t.Fatal("HTTP status triggered retry delay") }
	if exitCode := client.Run([]string{"exchange"}); exitCode == 0 {
		t.Fatal("exchange unexpectedly succeeded")
	}
	if requests != 1 {
		t.Fatalf("HTTP 503 produced %d requests, want 1", requests)
	}
}

func testApp(t *testing.T, baseURL string, stdin string, random io.Reader) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	home := t.TempDir()
	client := &App{
		BaseURL:       baseURL,
		HTTPClient:    http.DefaultClient,
		Store:         credentials.NewStore(home),
		ListenStore:   listenstore.NewStore(home),
		Stdin:         strings.NewReader(stdin),
		Stdout:        stdout,
		Stderr:        stderr,
		Random:        random,
		RetryAttempts: 3,
		RetryDelay:    func(int) {},
	}
	return client, stdout, stderr
}

func deterministicRandom(values ...byte) io.Reader {
	var contents []byte
	for _, value := range values {
		contents = append(contents, bytes.Repeat([]byte{value}, 32)...)
	}
	return bytes.NewReader(contents)
}

func repeatedHex(value byte) string {
	return hex.EncodeToString(bytes.Repeat([]byte{value}, 32))
}

type failOnceTransport struct {
	base   http.RoundTripper
	calls  int
	bodies [][]byte
}

func (t *failOnceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if err := request.Body.Close(); err != nil {
		return nil, err
	}
	t.bodies = append(t.bodies, body)
	if t.calls == 1 {
		return nil, errors.New("simulated transport failure")
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return t.base.RoundTrip(request)
}

func testCredential() credentials.Credential {
	return credentials.Credential{
		APIToken:       "api_" + repeatedHex(0xa1),
		SocketKey:      "sock_" + repeatedHex(0xb2),
		WorkstreamCode: "694",
		AgentID:        "agent-7",
		SocketAddress:  "wss://socket.example/agent-7",
	}
}
