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
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const (
	maxTicketBytes              = 16 * 1024
	maxResponseBytes            = 4 * 1024 * 1024
	maxMessagePageResponseBytes = 24 * 1024 * 1024
)

var validWorkstreamCode = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type App struct {
	BaseURL         string
	HTTPClient      *http.Client
	Store           *credentials.Store
	ListenStore     *listenstore.Store
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	Random          io.Reader
	RetryAttempts   int
	RetryDelay      func(attempt int)
	ListenPollLimit int
	ListenSleep     func(delay time.Duration)
}

type publicError struct {
	message string
}

func (e *publicError) Error() string {
	return e.message
}

type silentError struct{}

func (*silentError) Error() string {
	return "listen stopped"
}

type transportFailure struct {
	reason        string
	publicMessage string
}

func (*transportFailure) Error() string {
	return "AirCommand transport failure"
}

type exchangeRequest struct {
	TicketSecret  string `json:"ticketSecret"`
	APIToken      string `json:"apiToken"`
	SocketKey     string `json:"socketKey"`
	IdempotencyID string `json:"idempotencyId"`
}

type updateRequest struct {
	Body          string `json:"body"`
	IdempotencyID string `json:"idempotencyId"`
}

type messageSendRequest struct {
	RecipientID   string `json:"recipientId"`
	Body          string `json:"body"`
	IdempotencyID string `json:"idempotencyId"`
}

type workstreamRoster struct {
	Collaborators []rosterCollaborator `json:"collaborators"`
}

type rosterCollaborator struct {
	AccountID string        `json:"accountId"`
	Name      string        `json:"name"`
	Agents    []rosterAgent `json:"agents"`
}

type rosterAgent struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

type serviceErrorResponse struct {
	Message string `json:"message"`
	Error   string `json:"error"`
	Code    string `json:"code"`
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

type messageNotification struct {
	Type     string `json:"type"`
	UpdateID string `json:"updateId"`
	Author   string `json:"author"`
	TaskID   string `json:"taskId"`
	At       string `json:"at"`
	Summary  string `json:"summary"`
}

type messagesResponse struct {
	Notifications    []messageNotification `json:"notifications"`
	Cursor           *string               `json:"cursor"`
	PollAfterSeconds *int                  `json:"pollAfterSeconds"`
}

type httpResult struct {
	status int
	body   []byte
}

func (a *App) Run(arguments []string) int {
	var err error
	if help, ok := requestedHelp(arguments); ok {
		_, err = fmt.Fprintln(a.outputWriter(), help)
		if err != nil {
			err = &publicError{message: "Unable to write help output."}
		}
	} else if len(arguments) == 0 {
		err = &publicError{message: usage()}
	} else {
		switch arguments[0] {
		case "exchange":
			err = a.exchange(arguments[1:])
		case "send":
			err = a.send(arguments[1:])
		case "update":
			err = a.update(arguments[1:])
		case "read":
			err = a.read(arguments[1:])
		case "inbox":
			err = a.inbox(arguments[1:])
		case "ack":
			err = a.ack(arguments[1:])
		case "listen":
			err = a.listen(arguments[1:])
		default:
			err = &publicError{message: usage()}
		}
	}

	if err == nil {
		return 0
	}
	if _, silent := err.(*silentError); silent {
		return 1
	}
	message := "AirCommand command failed."
	if visible, ok := err.(*publicError); ok {
		message = visible.message
	}
	_, _ = fmt.Fprintln(a.errorWriter(), message)
	return 1
}

func usage() string {
	return "Usage: ac-cli exchange | send --workstream <code> [--agent <agentId>] --to <agentId|name> --body <text> | update --workstream <code> [--agent <agentId>] --body <text> | read --workstream <code> [--agent <agentId>] | inbox --workstream <code> [--agent <agentId>] [--all] [--limit N] [--cursor C] | ack --workstream <code> [--agent <agentId>] --message <messageId> | listen --workstream <code> [--agent <agentId>]"
}

func requestedHelp(arguments []string) (string, bool) {
	if len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h") {
		return usage(), true
	}
	if len(arguments) != 2 || (arguments[1] != "--help" && arguments[1] != "-h") {
		return "", false
	}
	switch arguments[0] {
	case "exchange":
		return "Usage: ac-cli exchange (supply the ticket on standard input)", true
	case "send":
		return "Usage: ac-cli send --workstream <code> [--agent <agentId>] --to <agentId|name> --body <text>", true
	case "update":
		return "Usage: ac-cli update --workstream <code> [--agent <agentId>] --body <text>", true
	case "read":
		return "Usage: ac-cli read --workstream <code> [--agent <agentId>]", true
	case "inbox":
		return inboxUsage, true
	case "ack":
		return ackUsage, true
	case "listen":
		return "Usage: ac-cli listen --workstream <code> [--agent <agentId>]", true
	default:
		return "", false
	}
}

func (a *App) exchange(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli exchange (supply the ticket on standard input)"}
	}
	if a.Store == nil {
		return &publicError{message: "Credential storage is unavailable."}
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
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
		return storageError(err, "Enrollment succeeded, but the credential file could not be saved securely.")
	}

	protected := []string{ticket, apiToken, socketKey}
	output := fmt.Sprintf(
		"Agent ID: %s\nUse for send/update/read/inbox/ack/listen: --agent %s\nAgent name: %s\nWorkstream: %s\nSocket address: %s\n",
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
	var recipient string
	var body string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "sending agent ID")
	flags.StringVar(&recipient, "to", "", "recipient agent ID or name")
	flags.StringVar(&body, "body", "", "message body")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" || strings.TrimSpace(recipient) == "" || body == "" {
		return &publicError{message: "Usage: ac-cli send --workstream <code> [--agent <agentId>] --to <agentId|name> --body <text>"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	resolvedRecipient, err := a.resolveMessageRecipient(workstreamCode, recipient, credential)
	if err != nil {
		return err
	}
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a message idempotency ID."}
	}
	payload, err := json.Marshal(messageSendRequest{
		RecipientID:   resolvedRecipient,
		Body:          body,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the message."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/messages"
	response, err := a.messageAPIRequest(http.MethodPost, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status != http.StatusCreated {
		return messageStatusError(response.status, response.body, workstreamCode, resolvedRecipient)
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}

func (a *App) update(arguments []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	var body string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&body, "body", "", "update body")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" || body == "" {
		return &publicError{message: "Usage: ac-cli update --workstream <code> [--agent <agentId>] --body <text>"}
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
	payload, err := json.Marshal(updateRequest{Body: body, IdempotencyID: idempotencyID})
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

func (a *App) resolveMessageRecipient(workstreamCode string, recipient string, credential credentials.Credential) (string, error) {
	if strings.HasPrefix(recipient, "agm_") || strings.HasPrefix(recipient, "ac_") {
		return recipient, nil
	}
	recipient = strings.TrimSpace(recipient)

	path := "/agent/v1/workstreams/" + workstreamCode
	response, err := a.request(http.MethodGet, path, credential.APIToken, nil)
	if err != nil {
		return "", err
	}
	if response.status < 200 || response.status >= 300 {
		return "", rosterStatusError(response.status, workstreamCode)
	}
	roster, err := decodeWorkstreamRoster(response.body)
	if err != nil {
		return "", &publicError{message: "The workstream service returned an invalid roster response."}
	}

	active := make([]rosterAgent, 0)
	for _, collaborator := range roster.Collaborators {
		for _, agent := range collaborator.Agents {
			if agent.Status == "active" {
				agent.Name = strings.TrimSpace(agent.Name)
				active = append(active, agent)
			}
		}
	}

	exact := matchingAgents(active, recipient, false)
	if len(exact) == 1 {
		return exact[0].AgentID, nil
	}
	if len(exact) > 1 {
		return "", ambiguousRecipientError(recipient, exact)
	}
	folded := matchingAgents(active, recipient, true)
	if len(folded) == 1 {
		return folded[0].AgentID, nil
	}
	if len(folded) > 1 {
		return "", ambiguousRecipientError(recipient, folded)
	}

	availableSet := make(map[string]struct{})
	for _, agent := range active {
		availableSet[agent.Name] = struct{}{}
	}
	available := make([]string, 0, len(availableSet))
	for name := range availableSet {
		available = append(available, singleLine(name))
	}
	sort.Strings(available)
	if len(available) == 0 {
		return "", &publicError{message: fmt.Sprintf("No active agent named %q was found in workstream %s. No active agent names are available.", singleLine(recipient), workstreamCode)}
	}
	return "", &publicError{message: fmt.Sprintf(
		"No active agent named %q was found in workstream %s. Available active agent names: %s.",
		singleLine(recipient),
		workstreamCode,
		strings.Join(available, ", "),
	)}
}

func decodeWorkstreamRoster(body []byte) (workstreamRoster, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var roster workstreamRoster
	if err := decoder.Decode(&roster); err != nil {
		return workstreamRoster{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return workstreamRoster{}, err
	}
	for _, collaborator := range roster.Collaborators {
		for _, agent := range collaborator.Agents {
			if agent.AgentID == "" || strings.TrimSpace(agent.Name) == "" {
				return workstreamRoster{}, errors.New("roster response has an incomplete agent")
			}
			switch agent.Status {
			case "active", "stopped", "removed":
			default:
				return workstreamRoster{}, errors.New("roster response has an invalid agent status")
			}
		}
	}
	return roster, nil
}

func matchingAgents(agents []rosterAgent, name string, fold bool) []rosterAgent {
	// Deliberately do not Unicode-normalize names. Operators use ASCII in
	// practice; EqualFold is the only fallback after exact matching.
	var matches []rosterAgent
	for _, agent := range agents {
		matched := agent.Name == name
		if fold {
			matched = strings.EqualFold(agent.Name, name)
		}
		if matched {
			matches = append(matches, agent)
		}
	}
	return matches
}

func ambiguousRecipientError(name string, matches []rosterAgent) error {
	sort.Slice(matches, func(i int, j int) bool {
		if matches[i].Name == matches[j].Name {
			return matches[i].AgentID < matches[j].AgentID
		}
		return matches[i].Name < matches[j].Name
	})
	candidates := make([]string, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, fmt.Sprintf("%s (%s)", singleLine(match.Name), singleLine(match.AgentID)))
	}
	return &publicError{message: fmt.Sprintf(
		"Agent name %q is ambiguous. Matching active agents: %s. Re-run with --to <agentId>.",
		singleLine(name),
		strings.Join(candidates, ", "),
	)}
}

func rosterStatusError(status int, workstreamCode string) error {
	switch status {
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found or is not available to this agent.", workstreamCode)}
	default:
		return &publicError{message: fmt.Sprintf("Unable to read the workstream roster (HTTP %d).", status)}
	}
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

func (a *App) listen(arguments []string) error {
	flags := flag.NewFlagSet("listen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: "Usage: ac-cli listen --workstream <code> [--agent <agentId>]"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	if a.ListenStore == nil {
		return &publicError{message: "Listener state storage is unavailable."}
	}

	cursor, hasStoredCursor, err := a.ListenStore.LoadCursor(credential.AgentID)
	if err != nil {
		return storageError(err, "Unable to read the listener cursor state.")
	}

	disconnected := false
	networkFailures := 0
	for poll := 1; ; poll++ {
		path := "/agent/v1/workstreams/" + workstreamCode + "/messages"
		if hasStoredCursor {
			query := url.Values{"since": []string{cursor}}
			path += "?" + query.Encode()
		}
		response, requestErr := a.singleRequest(http.MethodGet, path, credential.APIToken, nil)
		if requestErr != nil {
			var transport *transportFailure
			if !errors.As(requestErr, &transport) {
				return requestErr
			}

			reason := redact(singleLine(transport.reason), credential.APIToken, credential.SocketKey, cursor)
			if reason == "" {
				reason = "network error"
			}
			if err := a.writeActionLine("Lost connection: " + reason); err != nil {
				return err
			}
			disconnected = true
			networkFailures++
			if a.listenLimitReached(poll) {
				return nil
			}
			a.sleepForListen(networkBackoff(networkFailures))
			continue
		}

		switch response.status {
		case http.StatusUnauthorized:
			if err := a.writeActionLine(fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)); err != nil {
				return err
			}
			return &silentError{}
		case http.StatusNotFound:
			if err := a.writeActionLine(fmt.Sprintf("Workstream %s no longer exists.", workstreamCode)); err != nil {
				return err
			}
			return &silentError{}
		}
		if response.status < 200 || response.status >= 300 {
			return &publicError{message: fmt.Sprintf("AirCommand listener request failed (HTTP %d).", response.status)}
		}

		messages, err := decodeMessagesResponse(response.body)
		if err != nil {
			return &publicError{message: "The message service returned an invalid response."}
		}
		if disconnected {
			if err := a.writeActionLine("Connection restored."); err != nil {
				return err
			}
			disconnected = false
		}
		networkFailures = 0

		if hasStoredCursor {
			for _, notification := range messages.Notifications {
				if err := a.ListenStore.AppendNotification(credential.AgentID, notification); err != nil {
					return storageError(err, "Unable to append the AirCommand notification spool.")
				}
				if err := a.writeActionLine(notification.Summary); err != nil {
					return err
				}
			}
		}

		nextCursor := *messages.Cursor
		if !hasStoredCursor || nextCursor != cursor {
			if err := a.ListenStore.SaveCursor(credential.AgentID, nextCursor); err != nil {
				return storageError(err, "Unable to persist the listener cursor.")
			}
			cursor = nextCursor
			hasStoredCursor = true
		}
		if a.listenLimitReached(poll) {
			return nil
		}
		a.sleepForListen(pollDelay(messages.PollAfterSeconds))
	}
}

func decodeMessagesResponse(body []byte) (messagesResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var response messagesResponse
	if err := decoder.Decode(&response); err != nil {
		return messagesResponse{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return messagesResponse{}, err
	}
	if response.Cursor == nil {
		return messagesResponse{}, errors.New("message response is missing cursor")
	}
	for _, notification := range response.Notifications {
		if notification.UpdateID == "" || notification.At == "" || notification.Summary == "" {
			return messagesResponse{}, errors.New("message response has an incomplete notification")
		}
		if notification.Type != "workstream.message" && notification.Type != "task.message" {
			return messagesResponse{}, errors.New("message response has an unsupported notification type")
		}
	}
	return response, nil
}

func (a *App) writeActionLine(message string) error {
	if _, err := io.WriteString(a.outputWriter(), "[AirCommand] "+message+"\n"); err != nil {
		return &publicError{message: "Unable to write listener output."}
	}
	return nil
}

func pollDelay(seconds *int) time.Duration {
	if seconds == nil {
		return 30 * time.Second
	}
	if *seconds < 5 {
		return 5 * time.Second
	}
	maximumSeconds := int64((time.Duration(1<<63 - 1)) / time.Second)
	if int64(*seconds) > maximumSeconds {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(*seconds) * time.Second
}

func networkBackoff(failures int) time.Duration {
	switch failures {
	case 1:
		return 5 * time.Second
	case 2:
		return 10 * time.Second
	case 3:
		return 20 * time.Second
	default:
		return 30 * time.Second
	}
}

func (a *App) listenLimitReached(poll int) bool {
	return a.ListenPollLimit > 0 && poll >= a.ListenPollLimit
}

func (a *App) sleepForListen(delay time.Duration) {
	if a.ListenSleep != nil {
		a.ListenSleep(delay)
		return
	}
	time.Sleep(delay)
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
			if legacy := legacyStorageError(err); legacy != nil {
				return credentials.Credential{}, legacy
			}
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
	if legacy := legacyStorageError(err); legacy != nil {
		return credentials.Credential{}, legacy
	}
	var multiple *credentials.MultipleAgentsError
	if errors.As(err, &multiple) {
		agentIDs := make([]string, 0, len(multiple.AgentIDs))
		for _, availableAgentID := range multiple.AgentIDs {
			agentIDs = append(agentIDs, singleLine(availableAgentID))
		}
		return credentials.Credential{}, &publicError{message: fmt.Sprintf(
			"Multiple agents are enrolled on this machine. Available agent IDs: %s. Re-run for workstream %s with --agent <agentId>.",
			strings.Join(agentIDs, ", "),
			workstreamCode,
		)}
	}
	return credentials.Credential{}, &publicError{message: fmt.Sprintf("No stored credentials match workstream %s.", workstreamCode)}
}

func storageError(err error, fallback string) error {
	if legacy := legacyStorageError(err); legacy != nil {
		return legacy
	}
	return &publicError{message: fallback}
}

func legacyStorageError(err error) error {
	var legacy *storagepath.LegacyLayoutError
	if !errors.As(err, &legacy) {
		return nil
	}
	return &publicError{message: "The old AirCommand storage layout was found under ~/.aircommand. It will not be read or migrated. Remove the old credentials.json, state, and spool entries, then re-enroll this agent."}
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

func messageStatusError(status int, body []byte, workstreamCode string, recipientID string) error {
	response := serviceError(body)
	recipient := singleLine(recipientID)
	switch status {
	case http.StatusBadRequest:
		switch response.Message {
		case "recipientId is required":
			return &publicError{message: "A message recipient is required."}
		case "message body is required":
			return &publicError{message: "A message body is required."}
		case "missing idempotency key":
			return &publicError{message: "AirCommand rejected the message because its idempotency ID was missing."}
		case "idempotency key is too long":
			return &publicError{message: "AirCommand rejected the message because its idempotency ID was too long."}
		default:
			return &publicError{message: "AirCommand rejected the message request as invalid."}
		}
	case http.StatusUnauthorized:
		return &publicError{message: "The sending agent is no longer authorized. Re-enroll it before sending another message."}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found or this agent is not bound to it.", workstreamCode)}
	case http.StatusRequestTimeout:
		return &publicError{message: "Message delivery is uncertain: AirCommand timed out before acceptance was confirmed after retries."}
	case http.StatusConflict:
		switch response.Code {
		case "WorkstreamPaused":
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; message send rejected.", workstreamCode)}
		case "RecipientStopped":
			return &publicError{message: fmt.Sprintf("Recipient %s is stopped. Reconnect it before sending.", recipient)}
		case "RecipientRemoved":
			return &publicError{message: fmt.Sprintf("Recipient %s has been removed and cannot receive messages.", recipient)}
		case "RecipientNotActive":
			return &publicError{message: fmt.Sprintf("Recipient %s is not active and cannot receive messages.", recipient)}
		case "RecipientAmbiguous":
			return &publicError{message: fmt.Sprintf("Recipient ID %s is ambiguous in workstream %s; message send refused.", recipient, workstreamCode)}
		case "IdempotencyConflict":
			return &publicError{message: "AirCommand rejected the send because its idempotency ID was already used for a different message. The original message was not changed."}
		default:
			return &publicError{message: "AirCommand rejected the message because of a conflict (HTTP 409)."}
		}
	case http.StatusRequestEntityTooLarge:
		return &publicError{message: "Message body exceeds the 32768-byte limit; shorten it and try again."}
	case http.StatusUnprocessableEntity:
		return &publicError{message: fmt.Sprintf("Recipient %s is not a participant of workstream %s.", recipient, workstreamCode)}
	case http.StatusInternalServerError:
		return &publicError{message: "AirCommand could not complete the message send after retries (HTTP 500)."}
	case http.StatusServiceUnavailable:
		switch response.Code {
		case "ServiceUnavailable":
			return &publicError{message: "Message delivery is uncertain: AirCommand authentication remained unavailable after retries."}
		case "MessageSendUnavailable":
			return &publicError{message: "Message delivery is uncertain: AirCommand could not confirm message acceptance after retries."}
		default:
			return &publicError{message: "Message delivery is uncertain: AirCommand remained unavailable after retries (HTTP 503)."}
		}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand message send failed (HTTP %d).", status)}
	}
}

func serviceError(body []byte) serviceErrorResponse {
	var response serviceErrorResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return serviceErrorResponse{}
	}
	return response
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

func (a *App) messageAPIRequest(method string, path string, apiToken string, payload []byte) (httpResult, error) {
	return a.messageAPIRequestWithResponseLimit(method, path, apiToken, payload, maxResponseBytes)
}

func (a *App) messagePageRequest(path string, apiToken string) (httpResult, error) {
	// A valid 100-message page can exceed the ordinary response limit because
	// each 32 KiB body may expand when represented as a JSON string.
	return a.messageAPIRequestWithResponseLimit(http.MethodGet, path, apiToken, nil, maxMessagePageResponseBytes)
}

func (a *App) messageAPIRequestWithResponseLimit(method string, path string, apiToken string, payload []byte, responseLimit int64) (httpResult, error) {
	attempts := a.RetryAttempts
	if attempts <= 0 {
		attempts = 3
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := a.singleRequestWithResponseLimit(method, path, apiToken, payload, responseLimit)
		if err != nil {
			var transport *transportFailure
			if !errors.As(err, &transport) {
				return httpResult{}, err
			}
			// Once a final HTTP error status is known, a response-body transport
			// failure must not turn it into a retryable status.
			if result.status >= 300 && !messageStatusRetryable(result.status) {
				return result, nil
			}
			if attempt == attempts {
				return httpResult{}, &publicError{message: transport.publicMessage}
			}
		} else {
			if !messageStatusRetryable(result.status) || attempt == attempts {
				return result, nil
			}
		}
		a.waitBeforeRetry(attempt)
	}
	panic("unreachable")
}

func messageStatusRetryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
}

func (a *App) request(method string, path string, apiToken string, payload []byte) (httpResult, error) {
	attempts := a.RetryAttempts
	if attempts <= 0 {
		attempts = 3
	}

	var lastTransport *transportFailure
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := a.singleRequest(method, path, apiToken, payload)
		if err == nil {
			return result, nil
		}
		if !errors.As(err, &lastTransport) {
			return httpResult{}, err
		}
		if attempt < attempts {
			a.waitBeforeRetry(attempt)
		}
	}
	return httpResult{}, &publicError{message: lastTransport.publicMessage}
}

func (a *App) singleRequest(method string, path string, apiToken string, payload []byte) (httpResult, error) {
	return a.singleRequestWithResponseLimit(method, path, apiToken, payload, maxResponseBytes)
}

func (a *App) singleRequestWithResponseLimit(method string, path string, apiToken string, payload []byte, responseLimit int64) (httpResult, error) {
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

	response, err := configuredClient.Do(request)
	if err != nil {
		return httpResult{}, &transportFailure{
			reason:        networkErrorReason(err),
			publicMessage: "Unable to connect to AirCommand.",
		}
	}

	contents, readErr := readResponseWithLimit(response.Body, responseLimit)
	closeErr := response.Body.Close()
	result := httpResult{status: response.StatusCode, body: contents}
	if errors.Is(readErr, errResponseTooLarge) {
		return httpResult{}, &publicError{message: "The AirCommand response is too large."}
	}
	if readErr != nil {
		return result, &transportFailure{
			reason:        networkErrorReason(readErr),
			publicMessage: "Unable to read the AirCommand response.",
		}
	}
	if closeErr != nil {
		return result, &transportFailure{
			reason:        networkErrorReason(closeErr),
			publicMessage: "Unable to read the AirCommand response.",
		}
	}
	return result, nil
}

var errResponseTooLarge = errors.New("AirCommand response is too large")

func readResponse(reader io.Reader) ([]byte, error) {
	return readResponseWithLimit(reader, maxResponseBytes)
}

func readResponseWithLimit(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, errResponseTooLarge
	}
	return contents, nil
}

func networkErrorReason(err error) string {
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		return urlError.Err.Error()
	}
	return err.Error()
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
