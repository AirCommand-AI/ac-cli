package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

const (
	joinUsage  = "Usage: ac-cli join --workstream <code> [--name <agentName>] [--listen]"
	taskUsage  = "Usage: ac-cli task <id> --workstream <code> [--agent <agentId>]"
	tasksUsage = "Usage: ac-cli tasks --workstream <code> [--agent <agentId>] [--mine] [--status <status>]"
)

const (
	defaultDevicePollInterval = 5 * time.Second
	maxDevicePollDuration     = 10 * time.Minute
)

type startDeviceLoginRequest struct {
	MachineName string `json:"machineName"`
}

type startDeviceLoginResponse struct {
	UserCode        string `json:"userCode"`
	PollSecret      string `json:"pollSecret"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresAt       string `json:"expiresAt"`
	PollInterval    int    `json:"pollIntervalSeconds"`
}

type pollDeviceLoginRequest struct {
	PollSecret string `json:"pollSecret"`
}

type pollDeviceLoginResponse struct {
	Status         string `json:"status"`
	Token          string `json:"token"`
	OrganizationID string `json:"organizationId"`
}

type workstreamSummary struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type listWorkstreamsResponse struct {
	Workstreams []workstreamSummary `json:"workstreams"`
	NextCursor  string              `json:"nextCursor"`
}

type joinRequest struct {
	AgentName     string `json:"agentName"`
	APIToken      string `json:"apiToken"`
	SocketKey     string `json:"socketKey"`
	IdempotencyID string `json:"idempotencyId"`
}

type joinResponse struct {
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	WorkstreamCode string `json:"workstreamCode"`
	SocketAddress  string `json:"socketAddress"`
}

// login binds this machine to the operator's organization. It is the only
// command that needs a human, and it is needed once per machine.
func (a *App) login(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli login"}
	}
	if a.Store == nil {
		return &publicError{message: "Credential storage is unavailable."}
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	payload, err := json.Marshal(startDeviceLoginRequest{MachineName: machineName()})
	if err != nil {
		return &publicError{message: "Unable to prepare the login request."}
	}
	response, err := a.request(http.MethodPost, "/ajax/device/start", "", payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to start a login for this machine."}
	}
	var start startDeviceLoginResponse
	if err := json.Unmarshal(response.body, &start); err != nil || start.PollSecret == "" || start.UserCode == "" {
		return &publicError{message: "The login service returned an invalid response."}
	}

	fmt.Fprintf(a.outputWriter(), "Open %s and enter this code:\n\n    %s\n\nWaiting for approval...\n", start.VerificationURL, start.UserCode)

	interval := time.Duration(start.PollInterval) * time.Second
	if interval < defaultDevicePollInterval {
		interval = defaultDevicePollInterval
	}
	pollPayload, err := json.Marshal(pollDeviceLoginRequest{PollSecret: start.PollSecret})
	if err != nil {
		return &publicError{message: "Unable to prepare the login request."}
	}

	deadline := time.Now().Add(maxDevicePollDuration)
	for time.Now().Before(deadline) {
		a.sleep(interval)
		result, err := a.singleRequest(http.MethodPost, "/ajax/device/poll", "", pollPayload)
		if err != nil {
			continue
		}
		if result.status == http.StatusNotFound {
			return &publicError{message: "This login is no longer valid. Run ac-cli login again."}
		}
		if result.status < 200 || result.status >= 300 {
			return &publicError{message: "This login expired or was already used. Run ac-cli login again."}
		}
		var poll pollDeviceLoginResponse
		if err := json.Unmarshal(result.body, &poll); err != nil {
			return &publicError{message: "The login service returned an invalid response."}
		}
		if poll.Status == "pending" {
			continue
		}
		if poll.Token == "" || poll.OrganizationID == "" {
			return &publicError{message: "The login service returned an invalid response."}
		}
		if err := a.Store.SaveMachine(credentials.Machine{
			APIToken:       poll.Token,
			OrganizationID: poll.OrganizationID,
			CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return &publicError{message: "Unable to store the machine credential."}
		}
		fmt.Fprintf(a.outputWriter(), "This machine is now logged in.\nRun ac-cli workstreams to see what it can join.\n")
		return nil
	}
	return &publicError{message: "The code was not approved in time. Run ac-cli login again."}
}

// workstreams lists what this machine can see. Seeing a workstream is not the
// same as being in it; joining is what allows messaging.
func (a *App) workstreams(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli workstreams"}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodGet, "/v1/workstreams", machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status == http.StatusUnauthorized {
		return &publicError{message: "This machine's login is no longer valid. Run ac-cli login again."}
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to list workstreams."}
	}
	var list listWorkstreamsResponse
	if err := json.Unmarshal(response.body, &list); err != nil {
		return &publicError{message: "The AirCommand service returned an invalid response."}
	}
	// Name any agent stored before the name was kept locally, so the listing
	// says "you are Pi here" rather than printing a bare identifier.
	for _, workstream := range list.Workstreams {
		a.backfillAgentNames(workstream.Code)
	}
	local := a.localAgentsByWorkstream()
	writer := a.outputWriter()
	if len(list.Workstreams) == 0 {
		fmt.Fprintln(writer, "No workstreams in this organization.")
		return nil
	}
	// Every workstream is listed, including ones with no local agent -- those
	// are the joinable ones, and omitting them hides the only useful action.
	for _, workstream := range list.Workstreams {
		marker := " "
		suffix := ""
		if names := local[workstream.Code]; len(names) > 0 {
			marker = "*"
			suffix = "  (you are " + strings.Join(names, ", ") + " here)"
		}
		fmt.Fprintf(writer, "%s %-8s %s%s\n", marker, workstream.Code, workstream.Name, suffix)
	}
	if len(local) > 0 {
		fmt.Fprintln(writer, "\n* this machine already has an agent here; join is only needed for the unmarked ones")
	}
	return nil
}

// task reads one workstream detail payload and renders the selected task plus
// its task-scoped updates. The positional ID must precede all flags.
func (a *App) task(arguments []string) error {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
		return &publicError{message: taskUsage}
	}
	taskID := strings.TrimSpace(arguments[0])
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	if taskID == "" || flags.Parse(arguments[1:]) != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: taskUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+workstreamCode, credential.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamStatusError(response.status, responseCode(response.body), workstreamCode, false)
	}
	detail, err := decodeTaskDetail(response.body)
	if err != nil {
		return &publicError{message: "The workstream service returned an invalid task response."}
	}

	var selected *taskListItem
	for index := range detail.Tasks {
		if detail.Tasks[index].ID == taskID {
			selected = &detail.Tasks[index]
			break
		}
	}
	protected := []string{credential.APIToken, credential.SocketKey}
	if selected == nil {
		return &publicError{message: fmt.Sprintf(
			"Task %s was not found in workstream %s.",
			safeMetadata(taskID, protected...),
			safeMetadata(workstreamCode, protected...),
		)}
	}

	comments := make([]taskCommentItem, 0)
	for _, update := range detail.Updates {
		if update.TaskID == taskID {
			comments = append(comments, update)
		}
	}
	sort.SliceStable(comments, func(i int, j int) bool {
		if comments[i].CreatedAt == comments[j].CreatedAt {
			return comments[i].ID < comments[j].ID
		}
		return comments[i].CreatedAt < comments[j].CreatedAt
	})

	safe := func(value string) string { return safeMetadata(value, protected...) }
	orDash := func(value string) string {
		if value == "" {
			return "-"
		}
		return safe(value)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Title: %s\n", safe(selected.Title))
	fmt.Fprintf(&output, "Description: %s\n", orDash(selected.Description))
	fmt.Fprintf(&output, "Status: %s\n", safe(selected.Status))
	fmt.Fprintf(&output, "Assignee: %s\n", orDash(selected.Assignee))
	fmt.Fprintf(&output, "Created: %s\n", safe(selected.CreatedAt))
	fmt.Fprintf(&output, "Updated: %s\n", safe(selected.UpdatedAt))
	output.WriteString("Comments:\n")
	if len(comments) == 0 {
		fmt.Fprintf(&output, "No comments for task %s.\n", safe(taskID))
	} else {
		for _, comment := range comments {
			fmt.Fprintf(&output, "%s\t%s\t%s\n", safe(comment.CreatedAt), orDash(comment.Author), safe(comment.Body))
		}
	}
	if _, err := io.WriteString(a.outputWriter(), output.String()); err != nil {
		return &publicError{message: "Unable to write task output."}
	}
	return nil
}

// tasks reads the existing workstream detail and prints a stable tab-separated
// summary. Filtering happens locally so no additional server endpoint is needed.
func (a *App) tasks(arguments []string) error {
	flags := flag.NewFlagSet("tasks", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	var mine bool
	var status string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.BoolVar(&mine, "mine", false, "show only tasks assigned to the selected agent")
	flags.StringVar(&status, "status", "", "task status")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: tasksUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if status != "" && !validTaskListStatus(status) {
		return &publicError{message: "--status must be one of todo, in_flight, blocked, or landed."}
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+workstreamCode, credential.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamStatusError(response.status, responseCode(response.body), workstreamCode, false)
	}
	tasks, err := decodeTaskList(response.body)
	if err != nil {
		return &publicError{message: "The workstream service returned an invalid task response."}
	}

	protected := []string{credential.APIToken, credential.SocketKey}
	writer := a.outputWriter()
	matches := 0
	for _, task := range tasks {
		if mine && task.Assignee != credential.AgentID {
			continue
		}
		if status != "" && task.Status != status {
			continue
		}
		matches++
		assignee := task.Assignee
		if assignee == "" {
			assignee = "-"
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\n",
			safeMetadata(task.ID, protected...),
			safeMetadata(task.Status, protected...),
			safeMetadata(assignee, protected...),
			safeMetadata(task.Title, protected...),
		); err != nil {
			return &publicError{message: "Unable to write task output."}
		}
	}
	if matches == 0 {
		if _, err := fmt.Fprintln(writer, emptyTaskListMessage(workstreamCode, status, mine)); err != nil {
			return &publicError{message: "Unable to write task output."}
		}
	}
	return nil
}

func emptyTaskListMessage(workstreamCode string, status string, mine bool) string {
	switch {
	case mine && status != "":
		return fmt.Sprintf("No tasks assigned to this agent with status %s in workstream %s.", status, workstreamCode)
	case mine:
		return fmt.Sprintf("No tasks assigned to this agent in workstream %s.", workstreamCode)
	case status != "":
		return fmt.Sprintf("No tasks with status %s in workstream %s.", status, workstreamCode)
	default:
		return fmt.Sprintf("No tasks in workstream %s.", workstreamCode)
	}
}

// localAgentsByWorkstream names the agents this machine already owns, keyed by
// workstream. Naming them rather than only marking the row is what lets an
// agent recognise its own prior identity instead of inferring it.
func (a *App) localAgentsByWorkstream() map[string][]string {
	names := map[string][]string{}
	if a.Store == nil {
		return names
	}
	for _, agent := range a.Store.ListLocalAgents() {
		name := strings.TrimSpace(agent.AgentName)
		if name == "" {
			name = agent.AgentID
		}
		names[agent.WorkstreamCode] = append(names[agent.WorkstreamCode], name)
	}
	for code := range names {
		sort.Strings(names[code])
	}
	return names
}

// join creates an agent in a workstream and activates it. Its output matches
// exchange so that runtime adapters parse either identically.
func (a *App) join(arguments []string) error {
	flags := flag.NewFlagSet("join", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentName string
	var listen bool
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentName, "name", "", "name this agent takes in the workstream")
	flags.BoolVar(&listen, "listen", false, "keep running and listen for messages after joining")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: joinUsage}
	}
	workstreamCode = strings.TrimSpace(workstreamCode)
	agentName = strings.TrimSpace(agentName)
	if workstreamCode == "" {
		return &publicError{message: joinUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	// An agent outlives the session that created it. A restarted runtime
	// asking to join again means "give me my agent back", so reuse it rather
	// than creating a second one that strands the first with an inbox nobody
	// reads. Only a live holder forces a new identity.
	existing, err := a.agentToResume(workstreamCode, agentName)
	if err != nil {
		return err
	}
	if existing != nil {
		a.reportAgentIdentity(listen, existing.AgentID, existing.AgentName, workstreamCode, socketAddressForAgentID(existing.AgentID))
		if listen {
			return a.listen([]string{"--workstream", workstreamCode, "--agent", existing.AgentID})
		}
		return nil
	}
	if agentName == "" {
		return &publicError{message: fmt.Sprintf(
			"This machine has no agent in workstream %s yet. Pass --name to say what this agent should be called.",
			workstreamCode)}
	}

	random := a.randomReader()
	apiToken, err := secrets.Credential(random, "api_")
	if err != nil {
		return &publicError{message: "Unable to generate agent credentials."}
	}
	socketKey, err := secrets.Credential(random, "sock_")
	if err != nil {
		return &publicError{message: "Unable to generate agent credentials."}
	}
	idempotencyID, err := secrets.IdempotencyID(random)
	if err != nil {
		return &publicError{message: "Unable to generate a join idempotency ID."}
	}
	payload, err := json.Marshal(joinRequest{
		AgentName:     agentName,
		APIToken:      apiToken,
		SocketKey:     socketKey,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the join request."}
	}

	response, err := a.request(http.MethodPost, "/v1/workstreams/"+workstreamCode+"/agents", machine.APIToken, payload)
	if err != nil {
		return err
	}
	switch {
	case response.status == http.StatusUnauthorized:
		return &publicError{message: "This machine's login is no longer valid. Run ac-cli login again."}
	case response.status == http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found in this organization.", workstreamCode)}
	case response.status == http.StatusBadRequest:
		return &publicError{message: joinRejectionMessage(response.body)}
	case response.status < 200 || response.status >= 300:
		return &publicError{message: "Unable to join that workstream."}
	}

	var joined joinResponse
	if err := json.Unmarshal(response.body, &joined); err != nil || joined.AgentID == "" {
		return &publicError{message: "The AirCommand service returned an invalid join response."}
	}
	if err := a.Store.Save(credentials.Credential{
		APIToken:       apiToken,
		SocketKey:      socketKey,
		WorkstreamCode: joined.WorkstreamCode,
		AgentID:        joined.AgentID,
		SocketAddress:  joined.SocketAddress,
		AgentName:      joined.AgentName,
	}); err != nil {
		return &publicError{message: "Joined the workstream but could not store the agent credential."}
	}

	a.reportAgentIdentity(listen, joined.AgentID, joined.AgentName, joined.WorkstreamCode, joined.SocketAddress)
	if listen {
		return a.listen([]string{"--workstream", joined.WorkstreamCode, "--agent", joined.AgentID})
	}
	return nil
}

// reportAgentIdentity writes the identity block. Under --listen it goes to
// standard error, because standard output is then the wake-line stream a
// harness turns into notifications, and five lines of identity would each
// arrive as one.
func (a *App) reportAgentIdentity(listening bool, agentID, agentName, workstreamCode, socketAddress string) {
	writer := a.outputWriter()
	if listening {
		writer = a.errorWriter()
	}
	a.writeAgentIdentity(writer, agentID, agentName, workstreamCode, socketAddress)
}

// writeAgentIdentity is the one identity block both joining and resuming emit,
// so a runtime adapter parses either outcome identically.
func (a *App) writeAgentIdentity(writer io.Writer, agentID, agentName, workstreamCode, socketAddress string) {
	fmt.Fprintf(writer, "Agent ID: %s\n", agentID)
	fmt.Fprintf(writer, "Use for send/update/read/task/tasks/inbox/ack/listen: --agent %s\n", agentID)
	fmt.Fprintf(writer, "Agent name: %s\n", agentName)
	fmt.Fprintf(writer, "Workstream: %s\n", workstreamCode)
	fmt.Fprintf(writer, "Socket address: %s\n", socketAddress)
}

// agentToResume decides which stored agent, if any, this join should hand
// back. A nil agent with no error means nothing here can be resumed and a
// fresh one should be created.
//
// Without a name it answers only when there is exactly one candidate. Guessing
// between several would silently strand whichever agent it did not pick,
// leaving that one active and addressable with nobody listening as it, so it
// asks instead.
func (a *App) agentToResume(workstreamCode string, agentName string) (*credentials.LocalAgent, error) {
	a.backfillAgentNames(workstreamCode)

	candidates := a.localAgentsIn(workstreamCode)
	if agentName != "" {
		candidates = filterAgentsNamed(candidates, agentName)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	var free []credentials.LocalAgent
	var held []credentials.LocalAgent
	for _, agent := range candidates {
		if agentlock.Held(a.Store.Home(), agent.AgentID) {
			held = append(held, agent)
			continue
		}
		free = append(free, agent)
	}

	if len(free) == 1 {
		agent := free[0]
		return &agent, nil
	}
	if len(free) > 1 {
		return nil, &publicError{message: fmt.Sprintf(
			"This machine has more than one agent in workstream %s: %s. Pass --name to say which one this session is.",
			workstreamCode, strings.Join(agentLabels(free), ", "))}
	}
	// Everything that matched is in use by a live session, so this session
	// needs an identity of its own rather than one of theirs.
	if agentName != "" {
		return nil, &publicError{message: fmt.Sprintf(
			"This machine is already running an agent called %s in workstream %s. Choose a different name for this session.",
			held[0].AgentName, workstreamCode)}
	}
	return nil, &publicError{message: fmt.Sprintf(
		"Every agent this machine has in workstream %s is in use by a live session: %s. Pass --name to join as a new one.",
		workstreamCode, strings.Join(agentLabels(held), ", "))}
}

// localAgentsIn lists the stored agents this machine holds in one workstream.
func (a *App) localAgentsIn(workstreamCode string) []credentials.LocalAgent {
	var matches []credentials.LocalAgent
	if a.Store == nil {
		return matches
	}
	for _, agent := range a.Store.ListLocalAgents() {
		if agent.WorkstreamCode == workstreamCode {
			matches = append(matches, agent)
		}
	}
	return matches
}

// filterAgentsNamed selects agents answering to one name. Matching ignores
// case because addressing a message by name does, so two agents differing only
// in case could not both be addressed.
func filterAgentsNamed(agents []credentials.LocalAgent, agentName string) []credentials.LocalAgent {
	var matches []credentials.LocalAgent
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.AgentName), agentName) {
			matches = append(matches, agent)
		}
	}
	return matches
}

// agentLabels names agents for a human, falling back to the identifier for one
// stored before names were kept.
func agentLabels(agents []credentials.LocalAgent) []string {
	labels := make([]string, 0, len(agents))
	for _, agent := range agents {
		if name := strings.TrimSpace(agent.AgentName); name != "" {
			labels = append(labels, name)
			continue
		}
		labels = append(labels, agent.AgentID)
	}
	sort.Strings(labels)
	return labels
}

// backfillAgentNames records the name of any stored agent saved before the name
// was kept locally. Without it a restarted runtime cannot recognise an agent it
// enrolled through the older setup-link flow, and would join again as a
// duplicate. Each agent asks only about itself, using its own credential, and
// any failure is left alone rather than blocking the join.
func (a *App) backfillAgentNames(workstreamCode string) {
	if a.Store == nil {
		return
	}
	for _, agent := range a.Store.ListLocalAgents() {
		if agent.WorkstreamCode != workstreamCode || strings.TrimSpace(agent.AgentName) != "" {
			continue
		}
		credential, err := a.Store.FindByAgent(workstreamCode, agent.AgentID)
		if err != nil {
			continue
		}
		response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+workstreamCode, credential.APIToken, nil)
		if err != nil || response.status < 200 || response.status >= 300 {
			continue
		}
		roster, err := decodeWorkstreamRoster(response.body)
		if err != nil {
			continue
		}
		name, found := rosterNameFor(roster, agent.AgentID)
		if !found {
			continue
		}
		credential.AgentName = name
		_ = a.Store.Save(credential)
	}
}

// rosterNameFor finds one agent's own name in a workstream roster.
func rosterNameFor(roster workstreamRoster, agentID string) (string, bool) {
	for _, collaborator := range roster.Collaborators {
		for _, agent := range collaborator.Agents {
			if agent.AgentID == agentID {
				return agent.Name, true
			}
		}
	}
	return "", false
}

func socketAddressForAgentID(agentID string) string { return "ac:" + agentID }

func (a *App) machineCredential() (credentials.Machine, error) {
	if a.Store == nil {
		return credentials.Machine{}, &publicError{message: "Credential storage is unavailable."}
	}
	machine, err := a.Store.LoadMachine()
	if err != nil {
		if err == credentials.ErrNoMachineLogin {
			return credentials.Machine{}, &publicError{message: "This machine is not logged in to AirCommand. Run ac-cli login."}
		}
		return credentials.Machine{}, &publicError{message: "Unable to read this machine's login."}
	}
	return machine, nil
}

func (a *App) sleep(delay time.Duration) {
	if a.ListenSleep != nil {
		a.ListenSleep(delay)
		return
	}
	time.Sleep(delay)
}

func machineName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func joinRejectionMessage(body []byte) string {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if message := strings.TrimSpace(payload.Message); message != "" {
			return message
		}
		if message := strings.TrimSpace(payload.Error); message != "" {
			return message
		}
	}
	return "That join request was rejected."
}
