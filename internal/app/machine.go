package app

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

const (
	workstreamsUsage = "Usage: aircom workstreams --org <org>"
	joinUsage        = "Usage: aircom join --agent <agentId|name> --org <org> --workstream <code> [--listen]"
	taskByIDUsage    = "Usage: aircom task <id> --workstream <code> [--agent <agentId>] [--status <status>] [--comment <text>]"
	taskIDFlagUsage  = "Usage: aircom task --id <id> --workstream <code> [--agent <agentId>] [--status <status>] [--comment <text>]"
	taskCreateUsage  = "Usage: aircom task create --workstream <code> --title <text> [--description <text>] [--assignee <agentId|name>] [--status <status>] [--agent <agentId>]"
	taskUsage        = taskByIDUsage + "\n" + taskIDFlagUsage + "\n" + taskCreateUsage
	tasksUsage       = "Usage: aircom tasks --workstream <code> [--agent <agentId>] [--mine] [--status <status>]"
)

const (
	defaultDevicePollInterval = 5 * time.Second
	maxDevicePollDuration     = 10 * time.Minute
)

type redeemDeviceCodeRequest struct {
	Code        string `json:"code"`
	MachineName string `json:"machineName"`
	Platform    string `json:"platform"`
}

type redeemDeviceCodeResponse struct {
	Token      string `json:"token"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
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

// joinAgentRequest carries only the credentials the agent generated for itself.
// The agent's name and identity come from its registration, not from the join.
type joinAgentRequest struct {
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
// initMachine registers this machine to a human's AirCommand account.
//
// The code runs browser to terminal: the dashboard shows a short code and this
// waits for it, rather than printing one for the human to carry the other way.
// That means no polling and no URL to read out — the browser is opened here and
// the credential arrives as the direct answer to redeeming the code.
func (a *App) initMachine(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: aircom init"}
	}
	if a.Store == nil {
		return &publicError{message: "Credential storage is unavailable."}
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	codeURL := strings.TrimRight(a.BaseURL, "/") + "/device"
	fmt.Fprintf(a.outputWriter(), "Opening %s to get a code.\n", codeURL)
	// Failing to open a browser is not fatal: the human can open the page.
	if err := a.openBrowser(codeURL); err != nil {
		fmt.Fprintf(a.outputWriter(), "Could not open a browser. Open this page yourself:\n\n    %s\n", codeURL)
	}

	code, err := a.promptForCode()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(redeemDeviceCodeRequest{
		Code:        code,
		MachineName: machineName(),
		Platform:    platformName(),
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the registration request."}
	}
	response, err := a.request(http.MethodPost, "/ajax/device/redeem", "", payload)
	if err != nil {
		return err
	}
	if response.status == http.StatusBadRequest {
		return &publicError{message: "That code was not accepted. It may have expired or already been used — get a new one and run aircom init again."}
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to register this machine."}
	}
	var redeemed redeemDeviceCodeResponse
	if err := json.Unmarshal(response.body, &redeemed); err != nil || redeemed.Token == "" || redeemed.DeviceID == "" {
		return &publicError{message: "The registration service returned an invalid response."}
	}

	if err := a.Store.SaveMachine(credentials.Machine{
		APIToken:  redeemed.Token,
		DeviceID:  redeemed.DeviceID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return &publicError{message: "Unable to store the machine credential."}
	}

	fmt.Fprintf(a.outputWriter(), "\nThis machine is registered as %s.\n\nAgents you run here can now connect to AirCommand.\n", redeemed.DeviceID)
	return nil
}

// promptForCode reads the code the dashboard is showing.
func (a *App) promptForCode() (string, error) {
	fmt.Fprint(a.outputWriter(), "\nEnter the code shown in your browser: ")
	reader := bufio.NewReader(a.inputReader())
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", &publicError{message: "No code was entered."}
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return "", &publicError{message: "No code was entered."}
	}
	return code, nil
}

// openBrowser asks the desktop to open a URL. Best effort by design: a headless
// or locked-down machine simply gets told to open the page itself.
func (a *App) openBrowser(url string) error {
	if a.OpenBrowser != nil {
		return a.OpenBrowser(url)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// workstreams lists what this machine can see. Seeing a workstream is not the
// same as being in it; joining is what allows messaging.
func (a *App) workstreams(arguments []string) error {
	flags := flag.NewFlagSet("workstreams", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var organizationReference string
	flags.StringVar(&organizationReference, "org", "", "organization name or id, as shown by aircom orgs")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: workstreamsUsage}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	// Workstreams belong to an organization and a machine can reach several,
	// so one has to be named; there is deliberately no default.
	organizationID, err := a.resolveOrganization(organizationReference)
	if err != nil {
		return err
	}
	previousOrganization := a.Organization
	a.Organization = organizationID
	defer func() { a.Organization = previousOrganization }()

	response, err := a.request(http.MethodGet, "/v1/workstreams", machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status == http.StatusUnauthorized {
		return &publicError{message: "This machine's registration is no longer valid. Run aircom init again."}
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

// task gives a leading literal "create" subcommand precedence. The --id form
// remains an explicit escape hatch for reading or mutating a task whose ID is create.
func (a *App) task(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "create" {
		return a.createTask(arguments[1:])
	}
	return a.taskByID(arguments)
}

// taskByID reads one workstream detail payload and renders the selected task
// plus its task-scoped updates. A positional ID must precede all flags.
func (a *App) taskByID(arguments []string) error {
	taskID := ""
	flagArguments := arguments
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		taskID = strings.TrimSpace(arguments[0])
		flagArguments = arguments[1:]
	}
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var explicitTaskID string
	var workstreamCode string
	var agentID string
	var status string
	var comment string
	flags.StringVar(&explicitTaskID, "id", "", "explicit task ID")
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&status, "status", "", "new task status")
	flags.StringVar(&comment, "comment", "", "task comment")
	if flags.Parse(flagArguments) != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: taskUsage}
	}
	explicitTaskID = strings.TrimSpace(explicitTaskID)
	if taskID != "" && explicitTaskID != "" {
		return &publicError{message: taskUsage}
	}
	if taskID == "" {
		taskID = explicitTaskID
	}
	if taskID == "" {
		return &publicError{message: taskUsage}
	}
	commentSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "comment" {
			commentSet = true
		}
	})
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if status != "" && !validTaskListStatus(status) {
		return &publicError{message: "--status must be one of todo, in_flight, blocked, or landed."}
	}
	if commentSet && strings.TrimSpace(comment) == "" {
		return &publicError{message: "--comment must contain non-whitespace text."}
	}
	if commentSet && status != "" {
		return &publicError{message: "--comment and --status cannot be used together; run them as separate commands."}
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	if commentSet {
		return a.addTaskComment(workstreamCode, taskID, comment, credential)
	}
	if status != "" {
		return a.setTaskStatus(workstreamCode, taskID, status, credential)
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
	output.WriteString(formatTaskState(*selected, protected...))
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

func (a *App) createTask(arguments []string) error {
	flags := flag.NewFlagSet("task create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	var title string
	var description string
	var assignee string
	status := "todo"
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&title, "title", "", "task title")
	flags.StringVar(&description, "description", "", "task description")
	flags.StringVar(&assignee, "assignee", "", "task assignee")
	flags.StringVar(&status, "status", "todo", "task status")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: taskCreateUsage}
	}
	titleSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "title" {
			titleSet = true
		}
	})
	if !titleSet {
		return &publicError{message: taskCreateUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return &publicError{message: "--title must contain non-whitespace text."}
	}
	if !validTaskListStatus(status) {
		return &publicError{message: "--status must be one of todo, in_flight, blocked, or landed."}
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task creation idempotency ID."}
	}
	payload, err := json.Marshal(taskCreateRequest{
		Title: title, Description: description, Assignee: assignee,
		Status: status, IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare task creation."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/tasks"
	response, err := a.messageAPIRequest(http.MethodPost, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return taskCreateStatusError(response.status, response.body, workstreamCode)
	}
	created, err := decodeTaskResponse(response.body)
	if err != nil {
		return &publicError{message: "The workstream service returned an invalid task creation response."}
	}
	if _, err := fmt.Fprintf(a.outputWriter(), "Created task: %s\n", safeMetadata(created.ID, credential.APIToken, credential.SocketKey)); err != nil {
		return &publicError{message: "Unable to write task creation output."}
	}
	return nil
}

func (a *App) addTaskComment(workstreamCode string, taskID string, body string, credential credentials.Credential) error {
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task comment idempotency ID."}
	}
	payload, err := json.Marshal(taskCommentRequest{Body: body, TaskID: taskID, IdempotencyID: idempotencyID})
	if err != nil {
		return &publicError{message: "Unable to prepare the task comment."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/updates"
	response, err := a.messageAPIRequest(http.MethodPost, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		protectedTaskID := safeMetadata(taskID, credential.APIToken, credential.SocketKey)
		return taskCommentStatusError(response.status, response.body, workstreamCode, protectedTaskID)
	}
	comment, err := decodeTaskCommentResponse(response.body)
	if err != nil || comment.TaskID != taskID || comment.Body != body {
		return &publicError{message: "The workstream service returned an invalid task comment response."}
	}
	protected := []string{credential.APIToken, credential.SocketKey}
	safe := func(value string) string { return safeMetadata(value, protected...) }
	author := comment.Author
	if author == "" {
		author = "-"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Comment added: %s\n", safe(comment.ID))
	fmt.Fprintf(&output, "Task: %s\n", safe(comment.TaskID))
	fmt.Fprintf(&output, "Author: %s\n", safe(author))
	fmt.Fprintf(&output, "Created: %s\n", safe(comment.CreatedAt))
	fmt.Fprintf(&output, "Body: %s\n", safe(comment.Body))
	if _, err := io.WriteString(a.outputWriter(), output.String()); err != nil {
		return &publicError{message: "Unable to write task comment output."}
	}
	return nil
}

func (a *App) setTaskStatus(workstreamCode string, taskID string, status string, credential credentials.Credential) error {
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task status idempotency ID."}
	}
	payload, err := json.Marshal(taskStatusRequest{Status: status, IdempotencyID: idempotencyID})
	if err != nil {
		return &publicError{message: "Unable to prepare the task status change."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/tasks/" + url.PathEscape(taskID)
	response, err := a.messageAPIRequest(http.MethodPatch, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		protectedTaskID := safeMetadata(taskID, credential.APIToken, credential.SocketKey)
		return taskStatusError(response.status, response.body, workstreamCode, protectedTaskID)
	}
	updated, err := decodeTaskResponse(response.body)
	if err != nil || updated.ID != taskID || updated.Status != status {
		return &publicError{message: "The workstream service returned an invalid task status response."}
	}
	if _, err := io.WriteString(a.outputWriter(), formatTaskState(updated, credential.APIToken, credential.SocketKey)); err != nil {
		return &publicError{message: "Unable to write task output."}
	}
	return nil
}

func formatTaskState(task taskListItem, protected ...string) string {
	safe := func(value string) string { return safeMetadata(value, protected...) }
	orDash := func(value string) string {
		if value == "" {
			return "-"
		}
		return safe(value)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Title: %s\n", safe(task.Title))
	fmt.Fprintf(&output, "Description: %s\n", orDash(task.Description))
	fmt.Fprintf(&output, "Status: %s\n", safe(task.Status))
	fmt.Fprintf(&output, "Assignee: %s\n", orDash(task.Assignee))
	fmt.Fprintf(&output, "Created: %s\n", safe(task.CreatedAt))
	fmt.Fprintf(&output, "Updated: %s\n", safe(task.UpdatedAt))
	return output.String()
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
// join puts an agent that already exists into a workstream.
//
// It does not create one. An agent is registered by connect and outlives any
// particular workstream, which is what makes moving it expressible: leave, then
// join somewhere else, as the same agent with the same name and history.
func (a *App) join(arguments []string) error {
	flags := flag.NewFlagSet("join", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentReference string
	var organizationReference string
	var listen bool
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentReference, "agent", "", "agent id or name, as shown by aircom agents")
	flags.StringVar(&organizationReference, "org", "", "organization name or id, as shown by aircom orgs")
	flags.BoolVar(&listen, "listen", false, "keep running and listen for messages after joining")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: joinUsage}
	}
	workstreamCode = strings.TrimSpace(workstreamCode)
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

	organizationID, err := a.resolveOrganization(organizationReference)
	if err != nil {
		return err
	}
	agent, err := a.resolveAgent(agentReference)
	if err != nil {
		return err
	}
	if strings.TrimSpace(agent.WorkstreamCode) == workstreamCode {
		// Already there. Re-running join is how a restarted runtime asks for
		// its agent back, so report the identity rather than failing.
		a.reportAgentIdentity(listen, agent.AgentID, agent.Name, workstreamCode, socketAddressForAgentID(agent.AgentID))
		if listen {
			return a.listen([]string{"--workstream", workstreamCode, "--agent", agent.AgentID})
		}
		return nil
	}
	if strings.TrimSpace(agent.WorkstreamCode) != "" {
		return &publicError{message: fmt.Sprintf(
			"%s is already in workstream %s. Take it out first:\n    aircom leave --agent %s",
			agent.Name, agent.WorkstreamCode, agent.Name)}
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
	payload, err := json.Marshal(joinAgentRequest{
		APIToken:      apiToken,
		SocketKey:     socketKey,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the join request."}
	}

	previousOrganization := a.Organization
	a.Organization = organizationID
	response, err := a.request(http.MethodPost, "/v1/agents/"+agent.AgentID+"/workstreams/"+workstreamCode, machine.APIToken, payload)
	a.Organization = previousOrganization
	if err != nil {
		return err
	}
	switch {
	case response.status == http.StatusUnauthorized:
		return &publicError{message: "This machine's registration is no longer valid. Run aircom init again."}
	case response.status == http.StatusForbidden:
		return &publicError{message: "This machine is not allowed to act in that organization."}
	case response.status == http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found in that organization.", workstreamCode)}
	case response.status == http.StatusConflict:
		return &publicError{message: joinRejectionMessage(response.body)}
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
			return credentials.Machine{}, &publicError{message: "This machine is not registered with AirCommand. Run aircom init."}
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

// platformName reports os/arch so a human can tell two machines apart in their
// device list. Display only; the server never treats it as identity.
func platformName() string {
	return runtime.GOOS + "/" + runtime.GOARCH
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
