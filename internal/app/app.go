package app

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
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

type exchangeResponse struct {
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	SocketAddress  string `json:"socketAddress"`
	WorkstreamID   string `json:"workstreamId"`
	WorkstreamCode string `json:"workstreamCode"`
	Generation     *int   `json:"generation"`
	ConsumedAt     string `json:"consumedAt"`
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
	return "Usage: ac-cli exchange | send --workstream <code> [--agent <agentId>] --body <text> | read --workstream <code> [--agent <agentId>]"
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

	result, err := decodeExchangeResponse(response.body)
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
		"Agent ID: %s\nUse for send/read: --agent %s\nAgent name: %s\nWorkstream: %s\nSocket address: %s\n",
		safeMetadata(result.AgentID, protected...),
		safeMetadata(result.AgentID, protected...),
		safeMetadata(result.AgentName, protected...),
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
	var agentID string
	var body string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&body, "body", "", "update body")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" || body == "" {
		return &publicError{message: "Usage: ac-cli send --workstream <code> [--agent <agentId>] --body <text>"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
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
	var agentID string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: "Usage: ac-cli read --workstream <code> [--agent <agentId>]"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
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

func (a *App) credentialFor(workstreamCode string, agentID string) (credentials.Credential, error) {
	if a.Store == nil {
		return credentials.Credential{}, &publicError{message: "Credential storage is unavailable."}
	}
	if agentID != "" {
		credential, err := a.Store.FindByAgent(workstreamCode, agentID)
		if err != nil {
			return credentials.Credential{}, &publicError{message: fmt.Sprintf(
				"No stored credential matches agent %s in workstream %s.",
				singleLine(agentID),
				workstreamCode,
			)}
		}
		return credential, nil
	}

	credential, err := a.Store.FindByWorkstream(workstreamCode)
	if err == nil {
		return credential, nil
	}
	var multiple *credentials.MultipleAgentsError
	if errors.As(err, &multiple) {
		agentIDs := make([]string, 0, len(multiple.AgentIDs))
		for _, availableAgentID := range multiple.AgentIDs {
			agentIDs = append(agentIDs, singleLine(availableAgentID))
		}
		return credentials.Credential{}, &publicError{message: fmt.Sprintf(
			"Multiple credentials match workstream %s. Available agent IDs: %s. Re-run with --agent <agentId>.",
			workstreamCode,
			strings.Join(agentIDs, ", "),
		)}
	}
	return credentials.Credential{}, &publicError{message: fmt.Sprintf("No stored credentials match workstream %s.", workstreamCode)}
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
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return ""
	}
	return response.Code
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

func (a *App) waitBeforeRetry(attempt int) {
	if a.RetryDelay != nil {
		a.RetryDelay(attempt)
		return
	}
	time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
}

func decodeExchangeResponse(body []byte) (exchangeResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response exchangeResponse
	if err := decoder.Decode(&response); err != nil {
		return exchangeResponse{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return exchangeResponse{}, err
	}
	if response.AgentID == "" ||
		response.AgentName == "" ||
		response.SocketAddress == "" ||
		response.WorkstreamID == "" ||
		response.WorkstreamCode == "" ||
		response.Generation == nil ||
		response.ConsumedAt == "" {
		return exchangeResponse{}, errors.New("exchange response is missing a required field")
	}
	return response, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains more than one JSON value")
		}
		return err
	}
	return nil
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
