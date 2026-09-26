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

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const (
	// organizationHeader names the organization a device credential acts in.
	organizationHeader = "X-AC-Organization"

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
	// ListenNow reads the clock the listener measures outages by; tests drive
	// it with ListenSleep instead of waiting.
	ListenNow func() time.Time
	// OpenBrowser lets tests observe the page aircom init opens instead of
	// launching a real browser.
	OpenBrowser func(url string) error
	// Organization is sent on requests made with the device credential, which
	// carries no organization of its own. Set per command from --org; empty for
	// agent credentials, which are already bound to one workstream.
	Organization string
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

type taskCreateRequest struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Assignee      string   `json:"assignee"`
	Status        string   `json:"status"`
	IdempotencyID string   `json:"idempotencyId"`
	Number        int      `json:"number,omitempty"`
	Milestone     string   `json:"milestone,omitempty"`
	Type          string   `json:"type,omitempty"`
	Acceptance    []string `json:"acceptance,omitempty"`
	Validation    string   `json:"validation,omitempty"`
	DependsOn     []string `json:"dependsOn,omitempty"`
	Links         []string `json:"links,omitempty"`
}

type taskStatusRequest struct {
	Status        string `json:"status"`
	IdempotencyID string `json:"idempotencyId"`
	CancelReason  string `json:"cancelReason,omitempty"`
	ReplacedBy    string `json:"replacedBy,omitempty"`
}

// taskEditRequest changes a task's structured fields. An omitted field is left
// as it is; an empty one is cleared.
type taskEditRequest struct {
	Milestone     *string   `json:"milestone,omitempty"`
	Type          *string   `json:"type,omitempty"`
	Acceptance    *[]string `json:"acceptance,omitempty"`
	Validation    *string   `json:"validation,omitempty"`
	DependsOn     *[]string `json:"dependsOn,omitempty"`
	Links         *[]string `json:"links,omitempty"`
	IdempotencyID string    `json:"idempotencyId"`
}

type taskAssigneeRequest struct {
	Assignee      string `json:"assignee"`
	IdempotencyID string `json:"idempotencyId"`
}

type taskCommentRequest struct {
	Body          string `json:"body"`
	TaskID        string `json:"taskId"`
	IdempotencyID string `json:"idempotencyId"`
}

type messageSendRequest struct {
	RecipientID   string `json:"recipientId"`
	Body          string `json:"body"`
	IdempotencyID string `json:"idempotencyId"`
}

// workstreamRosterEnvelope matches the agent API's workstream detail response,
// which nests the roster under "workstream" alongside "tasks" and "updates".
// Decoding straight into workstreamRoster silently yields an empty roster,
// because encoding/json ignores unknown fields — so every name lookup fails and
// only literal agent IDs resolve.
type workstreamRosterEnvelope struct {
	Workstream workstreamRoster `json:"workstream"`
}

type taskListEnvelope struct {
	Tasks   []taskListItem    `json:"tasks"`
	Updates []taskCommentItem `json:"updates"`
}

type taskListItem struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Assignee    string     `json:"assignee"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	CreatedAt   string     `json:"createdAt"`
	UpdatedAt   string     `json:"updatedAt"`
	CreatedBy   *taskActor `json:"createdBy,omitempty"`
	AssignedBy  *taskActor `json:"assignedBy,omitempty"`
	AssignedAt  string     `json:"assignedAt,omitempty"`

	Number       int        `json:"number,omitempty"`
	Milestone    string     `json:"milestone,omitempty"`
	Type         string     `json:"type,omitempty"`
	Acceptance   []string   `json:"acceptance,omitempty"`
	Validation   string     `json:"validation,omitempty"`
	DependsOn    []string   `json:"dependsOn,omitempty"`
	Links        []string   `json:"links,omitempty"`
	CancelReason string     `json:"cancelReason,omitempty"`
	ReplacedBy   string     `json:"replacedBy,omitempty"`
	CancelledBy  *taskActor `json:"cancelledBy,omitempty"`
	CancelledAt  string     `json:"cancelledAt,omitempty"`
}

// taskActor is who created or changed a task, as the server recorded them.
type taskActor struct {
	Nature string `json:"nature"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

type taskCommentItem struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"taskId"`
	Author      string     `json:"author"`
	AuthorActor *taskActor `json:"authorActor,omitempty"`
	Body        string     `json:"body"`
	CreatedAt   string     `json:"createdAt"`
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
	Type         string `json:"type"`
	MessageID    string `json:"messageId"`
	SenderID     string `json:"senderId"`
	SenderNature string `json:"senderNature"`
	At           string `json:"at"`
	// Kind and TaskID are set for messages the service sends about a task:
	// assigned to this agent, or taken away from it. They say what the message
	// is about, not what to do; the agent still reads the message and the task.
	Kind   string `json:"kind,omitempty"`
	TaskID string `json:"taskId,omitempty"`
}

// Kinds of task message the listener words specially. Any other kind is shown
// as an ordinary message, so a kind added later still wakes the agent.
const (
	notificationKindTaskAssigned   = "task.assigned"
	notificationKindTaskUnassigned = "task.unassigned"
	notificationKindTaskCancelled  = "task.cancelled"
)

type spooledMessageNotification struct {
	Type         string `json:"type"`
	MessageID    string `json:"messageId"`
	SenderID     string `json:"senderId"`
	SenderNature string `json:"senderNature"`
	At           string `json:"at"`
	Kind         string `json:"kind,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
	Summary      string `json:"summary"`
}

type notificationFeedResponse struct {
	Notifications    []messageNotification `json:"notifications"`
	Cursor           *string               `json:"cursor"`
	PollAfterSeconds *int                  `json:"pollAfterSeconds"`
}

type senderIdentity struct {
	ID     string
	Nature string
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
		case "init":
			err = a.initMachine(arguments[1:])
		case "workstreams":
			err = a.workstreams(arguments[1:])
		case "connect":
			err = a.connect(arguments[1:])
		case "agents":
			err = a.agents(arguments[1:])
		case "orgs":
			err = a.orgs(arguments[1:])
		case "join":
			err = a.join(arguments[1:])
		case "leave":
			err = a.leave(arguments[1:])
		case "disconnect":
			err = a.disconnect(arguments[1:])
		case "exchange":
			err = a.exchange(arguments[1:])
		case "send":
			err = a.send(arguments[1:])
		case "update":
			err = a.update(arguments[1:])
		case "read":
			err = a.read(arguments[1:])
		case "task":
			err = a.task(arguments[1:])
		case "tasks":
			err = a.tasks(arguments[1:])
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
	return "Usage: aircom init | connect --name <agentName> | agents | orgs | join --agent <agentId|name> [--org <org> --workstream <code>] [--listen] | leave --agent <agentId|name> | disconnect --agent <agentId|name> | workstreams --org <org> [--agent <agentId|name>] | exchange | send --workstream <code> [--agent <agentId|name>] --to <agentId|name> --body <text> | update --workstream <code> [--agent <agentId|name>] --body <text> | read --workstream <code> [--agent <agentId|name>] | task <id> --workstream <code> [--agent <agentId|name>] [--status <status>] [--comment <text>] [--assignee <agentId|name>] | task --id <id> --workstream <code> [--agent <agentId|name>] [--status <status>] [--comment <text>] [--assignee <agentId|name>] | task create --workstream <code> --title <text> [--description <text>] [--assignee <agentId|name>] [--status <status>] [--agent <agentId|name>] | tasks --workstream <code> [--agent <agentId|name>] [--mine] [--status <status>] | inbox --workstream <code> [--agent <agentId|name>] [--all] [--limit N] [--cursor C] | ack --workstream <code> [--agent <agentId|name>] --message <messageId> | listen --workstream <code> [--agent <agentId|name>]"
}

func requestedHelp(arguments []string) (string, bool) {
	if len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h") {
		return usage(), true
	}
	if len(arguments) == 3 && arguments[0] == "task" && arguments[1] == "create" && (arguments[2] == "--help" || arguments[2] == "-h") {
		return taskCreateUsage, true
	}
	if len(arguments) != 2 || (arguments[1] != "--help" && arguments[1] != "-h") {
		return "", false
	}
	switch arguments[0] {
	case "init":
		return "Usage: aircom init", true
	case "connect":
		return connectUsage, true
	case "agents":
		return agentsUsage, true
	case "orgs":
		return orgsUsage, true
	case "leave":
		return leaveUsage, true
	case "disconnect":
		return disconnectUsage, true
	case "workstreams":
		return workstreamsUsage, true
	case "join":
		return joinUsage, true
	case "exchange":
		return "Usage: aircom exchange (supply the ticket on standard input)", true
	case "send":
		return "Usage: aircom send --workstream <code> [--agent <agentId|name>] --to <agentId|name> --body <text>", true
	case "update":
		return "Usage: aircom update --workstream <code> [--agent <agentId|name>] --body <text>", true
	case "read":
		return "Usage: aircom read --workstream <code> [--agent <agentId|name>]", true
	case "task":
		return taskUsage, true
	case "tasks":
		return tasksUsage, true
	case "inbox":
		return inboxUsage, true
	case "ack":
		return ackUsage, true
	case "listen":
		return "Usage: aircom listen --workstream <code> [--agent <agentId|name>]", true
	default:
		return "", false
	}
}

func (a *App) exchange(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: aircom exchange (supply the ticket on standard input)"}
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
		AgentName:      result.AgentName,
		AgentID:        result.AgentID,
		SocketAddress:  result.SocketAddress,
	}
	if err := a.Store.Save(credential); err != nil {
		return storageError(err, "Enrollment succeeded, but the credential file could not be saved securely.")
	}

	protected := []string{ticket, apiToken, socketKey}
	output := fmt.Sprintf(
		"Agent ID: %s\nUse for send/update/read/task/tasks/inbox/ack/listen: --agent %s\nAgent name: %s\nWorkstream: %s\nSocket address: %s\n",
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
		return &publicError{message: "Usage: aircom send --workstream <code> [--agent <agentId|name>] --to <agentId|name> --body <text>"}
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
		return &publicError{message: "Usage: aircom update --workstream <code> [--agent <agentId|name>] --body <text>"}
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
	var envelope workstreamRosterEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return workstreamRoster{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return workstreamRoster{}, err
	}
	roster := envelope.Workstream
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

func decodeTaskList(body []byte) ([]taskListItem, error) {
	envelope, err := decodeTaskEnvelope(body)
	if err != nil {
		return nil, err
	}
	if err := validateTaskItems(envelope.Tasks); err != nil {
		return nil, err
	}
	return envelope.Tasks, nil
}

func decodeTaskCommentResponse(body []byte) (taskCommentItem, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var comment taskCommentItem
	if err := decoder.Decode(&comment); err != nil {
		return taskCommentItem{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return taskCommentItem{}, err
	}
	if strings.TrimSpace(comment.ID) == "" ||
		strings.TrimSpace(comment.TaskID) == "" ||
		strings.TrimSpace(comment.Body) == "" ||
		strings.TrimSpace(comment.CreatedAt) == "" {
		return taskCommentItem{}, errors.New("task comment response is incomplete")
	}
	return comment, nil
}

func decodeTaskResponse(body []byte) (taskListItem, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var task taskListItem
	if err := decoder.Decode(&task); err != nil {
		return taskListItem{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return taskListItem{}, err
	}
	if err := validateTaskItems([]taskListItem{task}); err != nil {
		return taskListItem{}, err
	}
	if strings.TrimSpace(task.CreatedAt) == "" || strings.TrimSpace(task.UpdatedAt) == "" {
		return taskListItem{}, errors.New("task response has incomplete task detail")
	}
	return task, nil
}

func decodeTaskDetail(body []byte) (taskListEnvelope, error) {
	envelope, err := decodeTaskEnvelope(body)
	if err != nil {
		return taskListEnvelope{}, err
	}
	if err := validateTaskItems(envelope.Tasks); err != nil {
		return taskListEnvelope{}, err
	}
	if envelope.Updates == nil {
		return taskListEnvelope{}, errors.New("task response is missing updates")
	}
	for _, task := range envelope.Tasks {
		if strings.TrimSpace(task.CreatedAt) == "" || strings.TrimSpace(task.UpdatedAt) == "" {
			return taskListEnvelope{}, errors.New("task response has incomplete task detail")
		}
	}
	for _, update := range envelope.Updates {
		if strings.TrimSpace(update.ID) == "" || strings.TrimSpace(update.Body) == "" || strings.TrimSpace(update.CreatedAt) == "" {
			return taskListEnvelope{}, errors.New("task response has an invalid update")
		}
	}
	return envelope, nil
}

func decodeTaskEnvelope(body []byte) (taskListEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var envelope taskListEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return taskListEnvelope{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return taskListEnvelope{}, err
	}
	return envelope, nil
}

func validateTaskItems(tasks []taskListItem) error {
	if tasks == nil {
		return errors.New("task response is missing tasks")
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Title) == "" || !validResponseTaskStatus(task.Status) {
			return errors.New("task response has an invalid task")
		}
	}
	return nil
}

// validTaskListStatus reports whether status is one this CLI may send.
func validTaskListStatus(status string) bool {
	switch status {
	case "todo", "in_flight", "blocked", "landed", taskStatusCancelled:
		return true
	default:
		return false
	}
}

// validResponseTaskStatus accepts any status token the service returns, so a
// status added later is shown rather than breaking every task command.
func validResponseTaskStatus(status string) bool {
	if status == "" || len(status) > 32 {
		return false
	}
	for _, r := range status {
		if !(r >= 'a' && r <= 'z') && r != '_' {
			return false
		}
	}
	return true
}

const taskStatusCancelled = "cancelled"

// taskStatusChoices is the --status error message, naming every status.
const taskStatusChoices = "--status must be one of todo, in_flight, blocked, landed, or cancelled."

func (a *App) read(arguments []string) error {
	flags := flag.NewFlagSet("read", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: "Usage: aircom read --workstream <code> [--agent <agentId|name>]"}
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
		return &publicError{message: "Usage: aircom listen --workstream <code> [--agent <agentId|name>]"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	return a.listenAs(workstreamCode, agentID, nil)
}

// claimAgent takes the agent's lock: one live process per agent on this
// machine. Two would share the stored cursor, so whichever polled first would
// consume a notification and advance past it while the other never learned the
// message existed; and two joins racing for one agent would each save a
// credential over the other's.
func (a *App) claimAgent(agentID string) (*agentlock.Lock, error) {
	lock, err := agentlock.Acquire(a.Store.Home(), agentID)
	if err != nil {
		if errors.Is(err, agentlock.ErrHeld) {
			return nil, &publicError{message: fmt.Sprintf(
				"Agent %s is already running in another session on this machine. Stop that one before starting another.",
				agentID)}
		}
		return nil, storageError(err, "Unable to claim this agent.")
	}
	return lock, nil
}

// listenAs runs the listener. A caller that already holds the agent's lock
// passes it, and keeps ownership of it; otherwise the listener takes its own.
func (a *App) listenAs(workstreamCode string, agentID string, held *agentlock.Lock) error {
	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	if a.ListenStore == nil {
		return &publicError{message: "Listener state storage is unavailable."}
	}
	if held == nil {
		lock, err := a.claimAgent(credential.AgentID)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Release() }()
	}

	cursor, hasStoredCursor, err := a.ListenStore.LoadCursor(credential.AgentID, credential.WorkstreamKey())
	if err != nil {
		return storageError(err, "Unable to read the listener cursor state.")
	}

	var outage listenOutage
	networkFailures := 0
	senderNamesLoaded := false
	var senderNames map[senderIdentity]string
	for poll := 1; ; poll++ {
		path := "/agent/v1/workstreams/" + workstreamCode + "/notifications"
		if hasStoredCursor {
			query := url.Values{"since": []string{cursor}}
			path += "?" + query.Encode()
		}
		response, requestErr := a.singleRequest(http.MethodGet, path, credential.APIToken, nil)
		if terminalErr := a.notificationTerminalError(response.status, workstreamCode); terminalErr != nil {
			return terminalErr
		}
		if requestErr != nil {
			if response.status >= 300 && !notificationStatusRetryable(response.status) {
				return notificationStatusError(response.status, response.body)
			}

			reason := ""
			if notificationStatusRetryable(response.status) {
				reason = notificationFailureReason(response.status, response.body)
			} else {
				var transport *transportFailure
				if !errors.As(requestErr, &transport) {
					return requestErr
				}
				reason = redact(singleLine(transport.reason), credential.APIToken, credential.SocketKey, cursor)
				if reason == "" {
					reason = "network error"
				}
			}
			if err := a.noteListenFailure(&outage, reason); err != nil {
				return err
			}
			networkFailures++
			if a.listenLimitReached(poll) {
				return nil
			}
			a.sleepForListen(networkBackoff(networkFailures))
			continue
		}
		if notificationStatusRetryable(response.status) {
			if err := a.noteListenFailure(&outage, notificationFailureReason(response.status, response.body)); err != nil {
				return err
			}
			networkFailures++
			if a.listenLimitReached(poll) {
				return nil
			}
			a.sleepForListen(networkBackoff(networkFailures))
			continue
		}
		if response.status != http.StatusOK {
			return notificationStatusError(response.status, response.body)
		}

		feed, err := decodeNotificationFeedResponse(response.body)
		if err != nil {
			return &publicError{message: "The notification service returned an invalid response."}
		}
		if outage.recovered() {
			if err := a.writeActionLine("Connection restored."); err != nil {
				return err
			}
		}
		networkFailures = 0

		if hasStoredCursor {
			if len(feed.Notifications) > 0 && !senderNamesLoaded {
				senderNames = a.loadSenderNames(workstreamCode, credential)
				senderNamesLoaded = true
			}
			for _, notification := range feed.Notifications {
				summary := composeNotificationSummary(notification, workstreamCode, senderNames)
				spooled := spooledNotification(notification, summary)
				if err := a.ListenStore.AppendNotification(credential.AgentID, spooled); err != nil {
					return storageError(err, "Unable to append the AirCommand notification spool.")
				}
				if err := a.writeActionLine(summary); err != nil {
					return err
				}
			}
		}

		nextCursor := *feed.Cursor
		if !hasStoredCursor || nextCursor != cursor {
			if err := a.ListenStore.SaveCursor(credential.AgentID, credential.WorkstreamKey(), nextCursor); err != nil {
				return storageError(err, "Unable to persist the listener cursor.")
			}
			cursor = nextCursor
			hasStoredCursor = true
		}
		if a.listenLimitReached(poll) {
			return nil
		}
		a.sleepForListen(pollDelay(feed.PollAfterSeconds))
	}
}

func decodeNotificationFeedResponse(body []byte) (notificationFeedResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var response notificationFeedResponse
	if err := decoder.Decode(&response); err != nil {
		return notificationFeedResponse{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return notificationFeedResponse{}, err
	}
	if response.Notifications == nil || response.Cursor == nil || response.PollAfterSeconds == nil {
		return notificationFeedResponse{}, errors.New("notification response is missing a required field")
	}
	for _, notification := range response.Notifications {
		if notification.Type != "message.received" ||
			!validMessageID(notification.MessageID) ||
			strings.TrimSpace(notification.SenderID) == "" ||
			strings.TrimSpace(notification.At) == "" {
			return notificationFeedResponse{}, errors.New("notification response has an incomplete notification")
		}
		if notification.SenderNature != "agent" && notification.SenderNature != "human" {
			return notificationFeedResponse{}, errors.New("notification response has an invalid sender nature")
		}
		if notification.Kind != "" && !validNotificationTaskID(notification.TaskID) {
			return notificationFeedResponse{}, errors.New("notification response has a task message without a valid task ID")
		}
	}
	return response, nil
}

// validNotificationTaskID accepts a task ID that is safe to put in a wake line:
// short, and only letters, digits, '-' and '_'.
func validNotificationTaskID(taskID string) bool {
	if taskID == "" || len(taskID) > 64 {
		return false
	}
	for _, r := range taskID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (a *App) loadSenderNames(workstreamCode string, credential credentials.Credential) map[senderIdentity]string {
	path := "/agent/v1/workstreams/" + workstreamCode
	response, err := a.request(http.MethodGet, path, credential.APIToken, nil)
	if err != nil || response.status < 200 || response.status >= 300 {
		return nil
	}
	roster, err := decodeWorkstreamRoster(response.body)
	if err != nil {
		return nil
	}

	names := make(map[senderIdentity]string)
	ambiguous := make(map[senderIdentity]bool)
	cacheName := func(identity senderIdentity, name string) {
		name = strings.TrimSpace(name)
		if identity.ID == "" || name == "" || ambiguous[identity] {
			return
		}
		if existing, found := names[identity]; found && existing != name {
			delete(names, identity)
			ambiguous[identity] = true
			return
		}
		names[identity] = name
	}
	for _, collaborator := range roster.Collaborators {
		cacheName(senderIdentity{ID: collaborator.AccountID, Nature: "human"}, collaborator.Name)
		for _, agent := range collaborator.Agents {
			cacheName(senderIdentity{ID: agent.AgentID, Nature: "agent"}, agent.Name)
		}
	}
	return names
}

func composeNotificationSummary(notification messageNotification, workstreamCode string, senderNames map[senderIdentity]string) string {
	sender := notification.SenderID
	if name := senderNames[senderIdentity{ID: notification.SenderID, Nature: notification.SenderNature}]; name != "" {
		sender = name
	}
	switch notification.Kind {
	case notificationKindTaskAssigned:
		return fmt.Sprintf("Task %s assigned to you by %s (%s) in workstream %s: %s; run aircom inbox.",
			notification.TaskID, singleLine(sender), notification.SenderNature, workstreamCode, notification.MessageID)
	case notificationKindTaskUnassigned:
		return fmt.Sprintf("Task %s reassigned away from you by %s (%s) in workstream %s: %s; run aircom inbox.",
			notification.TaskID, singleLine(sender), notification.SenderNature, workstreamCode, notification.MessageID)
	case notificationKindTaskCancelled:
		return fmt.Sprintf("Task %s cancelled by %s (%s) in workstream %s: %s; run aircom inbox.",
			notification.TaskID, singleLine(sender), notification.SenderNature, workstreamCode, notification.MessageID)
	}
	return fmt.Sprintf(
		"New message from %s (%s) in workstream %s: %s; run aircom inbox.",
		singleLine(sender),
		notification.SenderNature,
		workstreamCode,
		notification.MessageID,
	)
}

func spooledNotification(notification messageNotification, summary string) spooledMessageNotification {
	return spooledMessageNotification{
		Type:         notification.Type,
		MessageID:    notification.MessageID,
		SenderID:     notification.SenderID,
		SenderNature: notification.SenderNature,
		At:           notification.At,
		Kind:         notification.Kind,
		TaskID:       notification.TaskID,
		Summary:      summary,
	}
}

func (a *App) notificationTerminalError(status int, workstreamCode string) error {
	switch status {
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
	default:
		return nil
	}
}

func notificationStatusRetryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
}

func notificationFailureReason(status int, body []byte) string {
	if status == http.StatusServiceUnavailable {
		switch serviceError(body).Code {
		case "ServiceUnavailable":
			return "AirCommand authentication service unavailable (HTTP 503)"
		case "NotificationFeedUnavailable":
			return "AirCommand notification feed unavailable (HTTP 503)"
		}
	}
	return fmt.Sprintf("AirCommand notification request failed (HTTP %d)", status)
}

func notificationStatusError(status int, body []byte) error {
	if status == http.StatusBadRequest && serviceError(body).Code == "InvalidNotificationCursor" {
		return &publicError{message: "The stored notification cursor is invalid. Restore a server-issued cursor or remove this agent's state.json to establish a new silent baseline."}
	}
	return &publicError{message: fmt.Sprintf("AirCommand listener request failed (HTTP %d).", status)}
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

// outageAnnounceAfter is how long polls must keep failing before the listener
// says so. Every line it prints wakes the agent and costs it a turn, so a few
// seconds' network hiccup is ridden out in silence.
const outageAnnounceAfter = 60 * time.Second

// listenOutage tracks one run of failed polls. It announces the run once it has
// lasted outageAnnounceAfter, and the recovery only if the loss was announced.
type listenOutage struct {
	since     time.Time
	announced bool
}

// failed records a failed poll at now and reports whether to announce the
// outage now.
func (o *listenOutage) failed(now time.Time) bool {
	if o.since.IsZero() {
		o.since = now
	}
	if o.announced || now.Sub(o.since) < outageAnnounceAfter {
		return false
	}
	o.announced = true
	return true
}

// recovered ends the run and reports whether to announce the recovery.
func (o *listenOutage) recovered() bool {
	announced := o.announced
	*o = listenOutage{}
	return announced
}

func (a *App) noteListenFailure(outage *listenOutage, reason string) error {
	if !outage.failed(a.listenNow()) {
		return nil
	}
	return a.writeActionLine("Lost connection: " + reason)
}

func (a *App) listenNow() time.Time {
	if a.ListenNow != nil {
		return a.ListenNow()
	}
	return time.Now()
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
		if err == nil {
			return credential, nil
		}
		if legacy := legacyStorageError(err); legacy != nil {
			return credentials.Credential{}, legacy
		}
		// IDs take precedence everywhere: a reference that is any local agent's
		// ID, or looks like one, is never read as a name, so an agent named after
		// another agent's ID can't be selected in its place.
		if a.isLocalAgentID(agentID) || looksLikeAgentID(agentID) {
			return credentials.Credential{}, a.noStoredAgentIDError(workstreamCode, agentID)
		}
		// Otherwise it is a name, matched among this workstream's agents only.
		resolvedID, err := a.storedAgentNamed(workstreamCode, agentID)
		if err != nil {
			return credentials.Credential{}, err
		}
		credential, err = a.Store.FindByAgent(workstreamCode, resolvedID)
		if err != nil {
			if legacy := legacyStorageError(err); legacy != nil {
				return credentials.Credential{}, legacy
			}
			return credentials.Credential{}, &publicError{message: fmt.Sprintf(
				"No stored credential matches agent %s in workstream %s.", singleLine(agentID), workstreamCode)}
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
			"Multiple agents are enrolled on this machine. Available agent IDs: %s. Re-run for workstream %s with --agent <agentId|name>.",
			strings.Join(agentIDs, ", "),
			workstreamCode,
		)}
	}
	return credentials.Credential{}, &publicError{message: fmt.Sprintf("No stored credentials match workstream %s.", workstreamCode)}
}

// storedAgentNamed resolves --agent given as a name to the ID of one agent
// stored on this machine for workstreamCode, with the same precedence as the
// machine-level commands: exact name, then case-insensitive name, and more
// than one match fails rather than guessing. Only that workstream's agents are
// considered, so a name never selects another workstream's credential.
func (a *App) storedAgentNamed(workstreamCode string, name string) (string, error) {
	name = strings.TrimSpace(name)
	here := a.localAgentsIn(workstreamCode)
	var exact []credentials.LocalAgent
	for _, agent := range here {
		if strings.TrimSpace(agent.AgentName) == name {
			exact = append(exact, agent)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = filterAgentsNamed(here, name)
	}
	switch len(matches) {
	case 1:
		return matches[0].AgentID, nil
	case 0:
		return "", a.noStoredAgentError(workstreamCode, name, here)
	default:
		ids := make([]string, 0, len(matches))
		for _, agent := range matches {
			ids = append(ids, agent.AgentID)
		}
		sort.Strings(ids)
		return "", &publicError{message: fmt.Sprintf(
			"More than one agent in workstream %s is called %q. Use its ID: --agent %s",
			workstreamCode, singleLine(name), strings.Join(ids, " or --agent "))}
	}
}

// agentIDPrefix begins every agent ID the service issues. A reference with it
// is an ID, never a name.
const agentIDPrefix = "agm_"

func looksLikeAgentID(reference string) bool {
	return strings.HasPrefix(strings.TrimSpace(reference), agentIDPrefix)
}

// isLocalAgentID reports whether reference is the ID of any agent stored on
// this machine, in any workstream.
func (a *App) isLocalAgentID(reference string) bool {
	for _, agent := range a.Store.ListLocalAgents() {
		if agent.AgentID == reference {
			return true
		}
	}
	return false
}

// noStoredAgentIDError refuses an agent ID with no credential in workstreamCode,
// saying where that agent is if it is on this machine.
func (a *App) noStoredAgentIDError(workstreamCode, agentID string) error {
	for _, agent := range a.Store.ListLocalAgents() {
		if agent.AgentID == agentID {
			return &publicError{message: fmt.Sprintf(
				"Agent %s is in workstream %s on this machine, not %s.", singleLine(agentID), agent.WorkstreamCode, workstreamCode)}
		}
	}
	return &publicError{message: fmt.Sprintf(
		"No agent with ID %s is in workstream %s on this machine.", singleLine(agentID), workstreamCode)}
}

// noStoredAgentError explains why a name matched nothing in the workstream,
// naming where an agent of that name is if it is on this machine in another one.
func (a *App) noStoredAgentError(workstreamCode, reference string, here []credentials.LocalAgent) error {
	for _, agent := range a.Store.ListLocalAgents() {
		if agent.WorkstreamCode == workstreamCode {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.AgentName), reference) {
			return &publicError{message: fmt.Sprintf(
				"Agent %s is in workstream %s on this machine, not %s.", singleLine(reference), agent.WorkstreamCode, workstreamCode)}
		}
	}
	if len(here) == 0 {
		return &publicError{message: fmt.Sprintf("No agent on this machine is in workstream %s.", workstreamCode)}
	}
	return &publicError{message: fmt.Sprintf(
		"No agent called %s is in workstream %s on this machine. Agents here: %s.",
		singleLine(reference), workstreamCode, strings.Join(agentLabels(here), ", "))}
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

func taskCreateStatusError(status int, body []byte, workstreamCode string) error {
	code := responseCode(body)
	switch status {
	case http.StatusBadRequest:
		return &publicError{message: "AirCommand rejected the task as invalid."}
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found.", workstreamCode)}
	case http.StatusRequestTimeout:
		return &publicError{message: "Task creation is uncertain: AirCommand timed out before confirming it after retries."}
	case http.StatusConflict:
		if code == "WorkstreamPaused" {
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; task creation rejected.", workstreamCode)}
		}
		if code == "TaskAssigneeAmbiguous" {
			return &publicError{message: "The task assignee name is ambiguous; use an active agent ID."}
		}
		return &publicError{message: "AirCommand rejected task creation because of a conflict (HTTP 409)."}
	case http.StatusUnprocessableEntity:
		if code == "TaskAssigneeNotFound" {
			return &publicError{message: "The task assignee does not match an active agent ID or name."}
		}
		return &publicError{message: "AirCommand could not process the task (HTTP 422)."}
	case http.StatusInternalServerError:
		return &publicError{message: "AirCommand could not create the task after retries (HTTP 500)."}
	case http.StatusServiceUnavailable:
		return &publicError{message: "Task creation is uncertain: AirCommand remained unavailable after retries (HTTP 503)."}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand task creation failed (HTTP %d).", status)}
	}
}

func taskCommentStatusError(status int, body []byte, workstreamCode string, taskID string) error {
	code := responseCode(body)
	switch status {
	case http.StatusBadRequest:
		return &publicError{message: "AirCommand rejected the task comment as invalid."}
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Task %s or workstream %s was not found.", singleLine(taskID), workstreamCode)}
	case http.StatusRequestTimeout:
		return &publicError{message: fmt.Sprintf("Comment delivery for task %s is uncertain: AirCommand timed out before confirming it after retries.", singleLine(taskID))}
	case http.StatusConflict:
		if code == "WorkstreamPaused" {
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; task comment rejected.", workstreamCode)}
		}
		return &publicError{message: "AirCommand rejected the task comment because of a conflict (HTTP 409)."}
	case http.StatusInternalServerError:
		return &publicError{message: "AirCommand could not add the task comment after retries (HTTP 500)."}
	case http.StatusServiceUnavailable:
		return &publicError{message: fmt.Sprintf("Comment delivery for task %s is uncertain: AirCommand remained unavailable after retries (HTTP 503).", singleLine(taskID))}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand task comment failed (HTTP %d).", status)}
	}
}

func taskStatusError(status int, body []byte, workstreamCode string, taskID string) error {
	code := responseCode(body)
	switch status {
	case http.StatusBadRequest:
		return &publicError{message: "AirCommand rejected the task status change as invalid."}
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Task %s was not found in workstream %s.", singleLine(taskID), workstreamCode)}
	case http.StatusRequestTimeout:
		return &publicError{message: fmt.Sprintf("Task %s status may have changed, but AirCommand timed out before confirming it after retries.", singleLine(taskID))}
	case http.StatusConflict:
		if code == "WorkstreamPaused" {
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; task status change rejected.", workstreamCode)}
		}
		return &publicError{message: "AirCommand rejected the task status change because of a conflict (HTTP 409)."}
	case http.StatusInternalServerError:
		return &publicError{message: "AirCommand could not complete the task status change after retries (HTTP 500)."}
	case http.StatusServiceUnavailable:
		return &publicError{message: fmt.Sprintf("Task %s status may have changed, but AirCommand remained unavailable after retries (HTTP 503).", singleLine(taskID))}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand task status change failed (HTTP %d).", status)}
	}
}

func taskAssigneeError(status int, body []byte, workstreamCode string, taskID string) error {
	code := responseCode(body)
	switch {
	case status == http.StatusConflict && code == "TaskAssigneeAmbiguous":
		return &publicError{message: "More than one agent has that name; use its agent ID."}
	case status == http.StatusUnprocessableEntity && code == "TaskAssigneeNotFound":
		return &publicError{message: "No active agent in this workstream has that ID or name."}
	case status == http.StatusConflict && code == "WorkstreamPaused":
		return &publicError{message: fmt.Sprintf("Workstream %s is paused; task reassignment rejected.", workstreamCode)}
	case status == http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case status == http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Task %s was not found in workstream %s.", singleLine(taskID), workstreamCode)}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand task reassignment failed (HTTP %d).", status)}
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
		// Closed is terminal, unlike paused: there is nothing to wait for.
		if write && code == "WorkstreamClosed" {
			return &publicError{message: fmt.Sprintf("Workstream %s is closed; write rejected.", workstreamCode)}
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
	// A device credential carries no organization, so every request that acts
	// in one has to name it. The server treats this as a selection, not a
	// grant: it is checked against the device's grant and its owner's
	// membership before it becomes scope.
	if a.Organization != "" {
		request.Header.Set(organizationHeader, a.Organization)
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
		return result, &publicError{message: "The AirCommand response is too large."}
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
