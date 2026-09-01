package app

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

const (
	maxTicketBytes   = 16 * 1024
	maxResponseBytes = 4 * 1024 * 1024
)

var validWorkstreamCode = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type App struct {
	BaseURL       string
	HTTPClient    *http.Client
	Store         *credentials.Store
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Random        io.Reader
	RetryAttempts int
	RetryDelay    func(attempt int)
}

type publicError struct {
	message string
}

func (e *publicError) Error() string {
	return e.message
}

type exchangeRequest struct {
	TicketSecret  string `json:"ticketSecret"`
	APIToken      string `json:"apiToken"`
	SocketKey     string `json:"socketKey"`
	IdempotencyID string `json:"idempotencyId"`
}

type sendRequest struct {
	Body          string `json:"body"`
	IdempotencyID string `json:"idempotencyId"`
}

type exchangeResult struct {
	AgentID        string
	AgentName      string
	WorkstreamCode string
	SocketAddress  string
}

type httpResult struct {
	status int
	body   []byte
}

func (a *App) Run(arguments []string) int {
	var err error
	if len(arguments) == 0 {
		err = &publicError{message: usage()}
	} else {
		switch arguments[0] {
		case "exchange":
			err = a.exchange(arguments[1:])
		case "send":
			err = a.send(arguments[1:])
		case "read":
			err = a.read(arguments[1:])
		default:
			err = &publicError{message: usage()}
		}
	}

	if err == nil {
		return 0
	}
	message := "AirCommand command failed."
	if visible, ok := err.(*publicError); ok {
		message = visible.message
	}
	_, _ = fmt.Fprintln(a.errorWriter(), message)
	return 1
}

func usage() string {
	return "Usage: ac-cli exchange | send --workstream <code> --body <text> | read --workstream <code>"
}

func (a *App) exchange(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli exchange (supply the ticket on standard input)"}
	}
	if a.Store == nil {
		return &publicError{message: "Credential storage is unavailable."}
	}

	ticket, err := readTicket(a.inputReader())
	if err != nil {
		return err
	}

	random := a.randomReader()
	apiToken, err := secrets.Credential(random, "api_")
	if err != nil {
		return &publicError{message: "Unable to generate enrollment credentials."}
	}
	socketKey, err := secrets.Credential(random, "sock_")
	if err != nil {
		return &publicError{message: "Unable to generate enrollment credentials."}
	}
	idempotencyID, err := secrets.IdempotencyID(random)
	if err != nil {
		return &publicError{message: "Unable to generate an enrollment idempotency ID."}
	}

	payload, err := json.Marshal(exchangeRequest{
		TicketSecret:  ticket,
		APIToken:      apiToken,
		SocketKey:     socketKey,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the enrollment exchange."}
	}
	response, err := a.request(http.MethodPost, "/ajax/enrollment/exchange", "", payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return exchangeStatusError(response.status)
	}

	result, err := decodeExchangeResult(response.body)
	if err != nil {
		return &publicError{message: "The enrollment service returned an invalid success response."}
	}
	credential := credentials.Credential{
		APIToken:       apiToken,
		SocketKey:      socketKey,
		WorkstreamCode: result.WorkstreamCode,
		AgentID:        result.AgentID,
		SocketAddress:  result.SocketAddress,
	}
	if err := a.Store.Save(credential); err != nil {
		return &publicError{message: "Enrollment succeeded, but the credential file could not be saved securely."}
	}

	protected := []string{ticket, apiToken, socketKey}
	output := fmt.Sprintf(
		"Agent name: %s\nAgent ID: %s\nWorkstream: %s\nSocket address: %s\n",
		safeMetadata(result.AgentName, protected...),
		safeMetadata(result.AgentID, protected...),
		safeMetadata(result.WorkstreamCode, protected...),
		safeMetadata(result.SocketAddress, protected...),
	)
	if _, err := io.WriteString(a.outputWriter(), output); err != nil {
		return &publicError{message: "Enrollment succeeded, but command output could not be written."}
	}
	return nil
}

func (a *App) send(arguments []string) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var body string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&body, "body", "", "update body")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" || body == "" {
		return &publicError{message: "Usage: ac-cli send --workstream <code> --body <text>"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode)
	if err != nil {
		return err
	}
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate an update idempotency ID."}
	}
	payload, err := json.Marshal(sendRequest{Body: body, IdempotencyID: idempotencyID})
	if err != nil {
		return &publicError{message: "Unable to prepare the workstream update."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/updates"
	response, err := a.request(http.MethodPost, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamStatusError(response.status, responseCode(response.body), workstreamCode, true)
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}

func (a *App) read(arguments []string) error {
	flags := flag.NewFlagSet("read", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: "Usage: ac-cli read --workstream <code>"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode)
	if err != nil {
		return err
	}
	path := "/agent/v1/workstreams/" + workstreamCode
	response, err := a.request(http.MethodGet, path, credential.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamStatusError(response.status, responseCode(response.body), workstreamCode, false)
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}

func readTicket(input io.Reader) (string, error) {
	contents, err := io.ReadAll(io.LimitReader(input, maxTicketBytes+1))
	if err != nil {
		return "", &publicError{message: "Unable to read the enrollment ticket from standard input."}
	}
	if len(contents) > maxTicketBytes {
		return "", &publicError{message: "The enrollment ticket from standard input is too large."}
	}
	ticket := strings.TrimSpace(string(contents))
	if ticket == "" {
		return "", &publicError{message: "No enrollment ticket was provided on standard input."}
	}
	return ticket, nil
}

func validateWorkstreamCode(code string) error {
	if !validWorkstreamCode.MatchString(code) {
		return &publicError{message: "The workstream code is invalid."}
	}
	return nil
}

func (a *App) credentialFor(workstreamCode string) (credentials.Credential, error) {
	if a.Store == nil {
		return credentials.Credential{}, &publicError{message: "Credential storage is unavailable."}
	}
	credential, err := a.Store.FindByWorkstream(workstreamCode)
	if err != nil {
		return credentials.Credential{}, &publicError{message: fmt.Sprintf("No stored credentials uniquely match workstream %s.", workstreamCode)}
	}
	return credential, nil
}

func exchangeStatusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return &publicError{message: "The enrollment ticket is invalid, expired, or already used."}
	case http.StatusConflict:
		return &publicError{message: "The enrollment exchange conflicted; retry the command with the same ticket."}
	default:
		return &publicError{message: fmt.Sprintf("Enrollment failed (HTTP %d).", status)}
	}
}

func workstreamStatusError(status int, code string, workstreamCode string, write bool) error {
	switch status {
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found or is not available to this agent.", workstreamCode)}
	case http.StatusConflict:
		if write && code == "WorkstreamPaused" {
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; write rejected.", workstreamCode)}
		}
	}
	return &publicError{message: fmt.Sprintf("AirCommand request failed (HTTP %d).", status)}
}

func responseCode(body []byte) string {
	var response struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return ""
	}
	if response.Code != "" {
		return response.Code
	}
	return response.Error.Code
}

func (a *App) request(method string, path string, apiToken string, payload []byte) (httpResult, error) {
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	configuredClient := *client
	if configuredClient.CheckRedirect == nil {
		configuredClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	client = &configuredClient
	attempts := a.RetryAttempts
	if attempts <= 0 {
		attempts = 3
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		request, err := http.NewRequest(method, strings.TrimRight(a.BaseURL, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return httpResult{}, &publicError{message: "The AirCommand service address is invalid."}
		}
		request.Header.Set("Accept", "application/json")
		if payload != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if apiToken != "" {
			request.Header.Set("Authorization", "Bearer "+apiToken)
		}

		response, err := client.Do(request)
		if err != nil {
			if attempt < attempts {
				a.waitBeforeRetry(attempt)
				continue
			}
			return httpResult{}, &publicError{message: "Unable to connect to AirCommand."}
		}

		contents, readErr := readResponse(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			if attempt < attempts {
				a.waitBeforeRetry(attempt)
				continue
			}
			return httpResult{}, &publicError{message: "Unable to read the AirCommand response."}
		}
		if retryableStatus(response.StatusCode) && attempt < attempts {
			a.waitBeforeRetry(attempt)
			continue
		}
		return httpResult{status: response.StatusCode, body: contents}, nil
	}
	return httpResult{}, &publicError{message: "Unable to connect to AirCommand."}
}

func readResponse(reader io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return contents, nil
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (a *App) waitBeforeRetry(attempt int) {
	if a.RetryDelay != nil {
		a.RetryDelay(attempt)
		return
	}
	time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
}

func decodeExchangeResult(body []byte) (exchangeResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return exchangeResult{}, err
	}

	scopes := []map[string]any{root}
	if data, ok := objectAt(root, "data"); ok {
		scopes = append([]map[string]any{data}, scopes...)
	}

	var result exchangeResult
	for _, scope := range scopes {
		if result.AgentID == "" {
			result.AgentID = firstString(scope, []string{"agentId"}, []string{"agentID"}, []string{"agent", "id"}, []string{"agent", "agentId"})
		}
		if result.AgentName == "" {
			result.AgentName = firstString(scope, []string{"agentName"}, []string{"agent", "name"}, []string{"agent", "agentName"})
		}
		if result.WorkstreamCode == "" {
			result.WorkstreamCode = firstString(scope, []string{"workstreamCode"}, []string{"workstream", "code"}, []string{"agent", "workstreamCode"})
		}
		if result.SocketAddress == "" {
			result.SocketAddress = firstString(
				scope,
				[]string{"socketAddress"},
				[]string{"socketUrl"},
				[]string{"socketURL"},
				[]string{"socketAddr"},
				[]string{"websocketUrl"},
				[]string{"websocketURL"},
				[]string{"socket", "address"},
				[]string{"socket", "url"},
			)
		}
	}
	if result.AgentID == "" || result.AgentName == "" || result.WorkstreamCode == "" || result.SocketAddress == "" {
		return exchangeResult{}, fmt.Errorf("exchange response is missing required metadata")
	}
	return result, nil
}

func objectAt(value map[string]any, key string) (map[string]any, bool) {
	object, ok := value[key].(map[string]any)
	return object, ok
}

func firstString(value map[string]any, paths ...[]string) string {
	for _, path := range paths {
		current := any(value)
		for _, key := range path {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[key]
		}
		switch item := current.(type) {
		case string:
			if item != "" {
				return item
			}
		case json.Number:
			return item.String()
		case float64:
			return strconv.FormatFloat(item, 'f', -1, 64)
		}
	}
	return ""
}

func writeSafeResponse(output io.Writer, body []byte, protected ...string) error {
	clean := redact(string(body), protected...)
	if clean == "" {
		clean = "OK"
	}
	if !strings.HasSuffix(clean, "\n") {
		clean += "\n"
	}
	if _, err := io.WriteString(output, clean); err != nil {
		return &publicError{message: "Unable to write command output."}
	}
	return nil
}

func redact(value string, protected ...string) string {
	filtered := make([]string, 0, len(protected))
	for _, secret := range protected {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	for _, secret := range filtered {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func safeMetadata(value string, protected ...string) string {
	return singleLine(redact(value, protected...))
}

func singleLine(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
}

func (a *App) inputReader() io.Reader {
	if a.Stdin != nil {
		return a.Stdin
	}
	return strings.NewReader("")
}

func (a *App) outputWriter() io.Writer {
	if a.Stdout != nil {
		return a.Stdout
	}
	return io.Discard
}

func (a *App) errorWriter() io.Writer {
	if a.Stderr != nil {
		return a.Stderr
	}
	return io.Discard
}

func (a *App) randomReader() io.Reader {
	if a.Random != nil {
		return a.Random
	}
	return rand.Reader
}
