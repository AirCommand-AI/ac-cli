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
	"strconv"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

const (
	workstreamsUsage = "Usage: aircom workstreams --org <org> [--agent <agentId|name>]"
	joinUsage        = "Usage: aircom join --agent <agentId|name> [--org <org> --workstream <code>] [--listen]"
	taskByIDUsage    = "Usage: aircom task <id|number> --workstream <code> [--agent <agentId|name>] [--status <status> [--reason <text>] [--replaced-by <id|number>]] [--comment <text>] [--assignee <agentId|name>] [--milestone <text>] [--type <text>] [--acceptance <text>]... [--validation <text>] [--depends-on <id|number>]... [--link <url>]..."
	taskIDFlagUsage  = "Usage: aircom task --id <id|number> --workstream <code> [same flags as above]"
	taskCreateUsage  = "Usage: aircom task create --workstream <code> --title <text> [--description <text>] [--assignee <agentId|name>] [--status <status>] [--number <n>] [--milestone <text>] [--type <text>] [--acceptance <text>]... [--validation <text>] [--depends-on <id|number>]... [--link <url>]... [--agent <agentId|name>]"
	taskUsage        = taskByIDUsage + "\n" + taskIDFlagUsage + "\n" + taskCreateUsage
	tasksUsage       = "Usage: aircom tasks --workstream <code> [--agent <agentId|name>] [--mine] [--status <status>] [--milestone <text>] [--type <text>]"
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
	var agentReference string
	flags.StringVar(&organizationReference, "org", "", "organization name or id, as shown by aircom orgs")
	flags.StringVar(&agentReference, "agent", "", "the agent asking, by name or id, so its workstreams are marked as yours")
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
	// Membership comes from the service's record of this machine's agents, not
	// from stored credentials: a credential carries only a code, and codes repeat
	// across organizations, so only the service can say which organization an
	// agent is in. The listing answers for the whole machine; only when the
	// caller says which agent it is can one of them be called "you".
	agents, err := a.fetchAgents()
	if err != nil {
		return err
	}
	var caller agentSummary
	if strings.TrimSpace(agentReference) != "" {
		if caller, err = matchAgent(agents, agentReference); err != nil {
			return err
		}
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
	local := agentsByWorkstream(agents, organizationID)
	writer := a.outputWriter()
	if len(list.Workstreams) == 0 {
		fmt.Fprintln(writer, "No workstreams in this organization.")
		return nil
	}
	// Every workstream is listed, including ones with no local agent -- those
	// are the joinable ones, and omitting them hides the only useful action.
	marked := false
	for _, workstream := range list.Workstreams {
		marker, suffix := workstreamMembership(local[workstream.Code], caller.AgentID)
		if marker == "*" {
			marked = true
		}
		fmt.Fprintf(writer, "%s %-8s %s%s\n", marker, workstream.Code, workstream.Name, suffix)
	}
	fmt.Fprint(writer, workstreamsFootnote(caller, marked, len(local) > 0))
	return nil
}

// workstreamMembership describes which of this machine's agents are in one
// workstream. Without a caller the "*" marks any agent from this machine; with
// one it marks only the caller, and the others are named as machine-mates.
func workstreamMembership(agents []localAgent, callerID string) (string, string) {
	var you string
	var others []string
	for _, agent := range agents {
		if callerID != "" && agent.ID == callerID {
			you = agent.Name
			continue
		}
		others = append(others, agent.Name)
	}
	onMachine := strings.Join(others, ", ")
	switch {
	case you != "" && len(others) > 0:
		return "*", "  (you are " + you + " here; also on this machine: " + onMachine + ")"
	case you != "":
		return "*", "  (you are " + you + " here)"
	case len(others) > 0 && callerID == "":
		return "*", "  (on this machine: " + onMachine + ")"
	case len(others) > 0:
		return " ", "  (on this machine: " + onMachine + ")"
	default:
		return " ", ""
	}
}

// workstreamsFootnote explains the marker. Every agent joins on its own, so a
// workstream holding another agent from this machine is still one the caller
// may need to join.
func workstreamsFootnote(caller agentSummary, marked, anyLocal bool) string {
	join := "Each agent joins on its own: aircom join --agent <name> --org <org> --workstream <code>"
	switch {
	case caller.AgentID != "" && marked:
		return "\n* marks workstreams " + caller.Name + " is in. " + join + "\n"
	case caller.AgentID != "":
		return "\n" + caller.Name + " is not in any of these workstreams. " + join + "\n"
	case anyLocal:
		return "\n* marks workstreams with an agent from this machine. " + join + "\n"
	default:
		return ""
	}
}

// task gives a leading literal "create" subcommand precedence. The --id form
// remains an explicit escape hatch for reading or mutating a task whose ID is create.
func (a *App) task(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "create" {
		return a.createTask(arguments[1:])
	}
	return a.taskByID(arguments)
}

// taskByID reads one task and its task-scoped updates, or changes it: its
// status, a comment, its assignee, or its structured fields, each as a separate
// command. The task is named by ID or number; a positional reference must
// precede all flags.
func (a *App) taskByID(arguments []string) error {
	taskID := ""
	flagArguments := arguments
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		taskID = strings.TrimSpace(arguments[0])
		flagArguments = arguments[1:]
	}
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var explicitTaskID, workstreamCode, agentID, status, comment, assignee, reason, replacedBy string
	var fields taskFieldFlags
	flags.StringVar(&explicitTaskID, "id", "", "explicit task ID")
	flags.StringVar(&assignee, "assignee", "", "hand the task to this agent")
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&status, "status", "", "new task status")
	flags.StringVar(&comment, "comment", "", "task comment")
	flags.StringVar(&reason, "reason", "", "why the task is cancelled")
	flags.StringVar(&replacedBy, "replaced-by", "", "the task replacing a cancelled one")
	fields.register(flags)
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
	set := map[string]bool{}
	flags.Visit(func(current *flag.Flag) { set[current.Name] = true })
	if err := validateTaskChange(set, workstreamCode, status, comment, assignee, reason); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	editSet := taskFieldsSet(set)
	if set["comment"] || status != "" || set["assignee"] || editSet {
		if taskID, err = a.resolveTaskID(workstreamCode, taskID, credential); err != nil {
			return err
		}
	}
	switch {
	case set["comment"]:
		return a.addTaskComment(workstreamCode, taskID, comment, credential)
	case status != "":
		return a.setTaskStatus(workstreamCode, taskID, taskStatusRequest{
			Status: status, CancelReason: strings.TrimSpace(reason), ReplacedBy: strings.TrimSpace(replacedBy),
		}, credential)
	case set["assignee"]:
		return a.setTaskAssignee(workstreamCode, taskID, strings.TrimSpace(assignee), credential)
	case editSet:
		return a.editTask(workstreamCode, taskID, fields.editRequest(set), credential)
	}
	return a.showTask(workstreamCode, taskID, credential)
}

// validateTaskChange checks that a task command asks for one kind of change,
// with the values that change needs.
func validateTaskChange(set map[string]bool, workstreamCode, status, comment, assignee, reason string) error {
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if status != "" && !validTaskListStatus(status) {
		return &publicError{message: taskStatusChoices}
	}
	switch {
	case set["comment"] && strings.TrimSpace(comment) == "":
		return &publicError{message: "--comment must contain non-whitespace text."}
	case set["comment"] && status != "":
		return &publicError{message: "--comment and --status cannot be used together; run them as separate commands."}
	case set["assignee"] && strings.TrimSpace(assignee) == "":
		return &publicError{message: "--assignee must name an agent."}
	case set["assignee"] && (set["comment"] || status != ""):
		return &publicError{message: "--assignee cannot be combined with --status or --comment; run them as separate commands."}
	case (set["reason"] || set["replaced-by"]) && status != taskStatusCancelled:
		return &publicError{message: "--reason and --replaced-by go only with --status cancelled."}
	case status == taskStatusCancelled && strings.TrimSpace(reason) == "":
		return &publicError{message: "--status cancelled requires --reason <text>."}
	case taskFieldsSet(set) && (set["comment"] || status != "" || set["assignee"]):
		return &publicError{message: "--milestone, --type, --acceptance, --validation, --depends-on and --link cannot be combined with --status, --comment or --assignee; run them as separate commands."}
	}
	return nil
}

func taskFieldsSet(set map[string]bool) bool {
	for name := range taskFieldNames {
		if set[name] {
			return true
		}
	}
	return false
}

// showTask prints one task, named by ID or number, and its comments oldest
// first.
func (a *App) showTask(workstreamCode string, ref string, credential credentials.Credential) error {
	detail, err := a.readTaskDetail(workstreamCode, credential)
	if err != nil {
		return err
	}
	selected := findTask(detail.Tasks, ref)
	if selected == nil {
		return taskNotFoundError(ref, workstreamCode, credential)
	}
	protected := []string{credential.APIToken, credential.SocketKey}

	comments := make([]taskCommentItem, 0)
	for _, update := range detail.Updates {
		if update.TaskID == selected.ID {
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
	var output strings.Builder
	output.WriteString(formatTaskState(*selected, detail.Tasks, protected...))
	output.WriteString("Comments:\n")
	if len(comments) == 0 {
		fmt.Fprintf(&output, "No comments for task %s.\n", safe(ref))
	} else {
		for _, comment := range comments {
			author := comment.Author
			if comment.AuthorActor != nil && comment.AuthorActor.Name != "" {
				author = comment.AuthorActor.Name
			}
			if author == "" {
				author = "-"
			}
			fmt.Fprintf(&output, "%s\t%s\t%s\n", safe(comment.CreatedAt), safe(author), safe(comment.Body))
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
	var number int
	var fields taskFieldFlags
	status := "todo"
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&title, "title", "", "task title")
	flags.StringVar(&description, "description", "", "task description")
	flags.StringVar(&assignee, "assignee", "", "task assignee")
	flags.StringVar(&status, "status", "todo", "task status")
	flags.IntVar(&number, "number", 0, "task number")
	fields.register(flags)
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: taskCreateUsage}
	}
	set := map[string]bool{}
	flags.Visit(func(current *flag.Flag) { set[current.Name] = true })
	if !set["title"] {
		return &publicError{message: taskCreateUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return &publicError{message: "--title must contain non-whitespace text."}
	}
	if !validTaskListStatus(status) {
		return &publicError{message: taskStatusChoices}
	}
	if status == taskStatusCancelled {
		return &publicError{message: "A task cannot be created cancelled."}
	}
	if set["number"] && (number < 1 || number > maxTaskNumber) {
		return &publicError{message: fmt.Sprintf("--number must be between 1 and %d.", maxTaskNumber)}
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
		Number: number, Milestone: fields.milestone, Type: fields.taskType, Acceptance: fields.acceptance.values,
		Validation: fields.validation, DependsOn: fields.dependsOn.values, Links: fields.links.values,
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
		if fieldErr := taskFieldError(response.body); fieldErr != nil {
			return fieldErr
		}
		return taskCreateStatusError(response.status, response.body, workstreamCode, credential)
	}
	created, err := decodeTaskResponse(response.body)
	if err != nil {
		return &publicError{message: "The workstream service returned an invalid task creation response."}
	}
	protected := []string{credential.APIToken, credential.SocketKey}
	var output strings.Builder
	fmt.Fprintf(&output, "Created task: %s\n", safeMetadata(created.ID, protected...))
	if created.Number > 0 {
		fmt.Fprintf(&output, "Number: #%d\n", created.Number)
	}
	if _, err := io.WriteString(a.outputWriter(), output.String()); err != nil {
		return &publicError{message: "Unable to write task creation output."}
	}
	a.warnMissingTaskChecks(created, workstreamCode, protected)
	return nil
}

// warnMissingTaskChecks reminds the creator, on stderr, that a task without
// acceptance criteria or validation cannot be checked when it lands.
func (a *App) warnMissingTaskChecks(task taskListItem, workstreamCode string, protected []string) {
	ref := task.ID
	if task.Number > 0 {
		ref = strconv.Itoa(task.Number)
	}
	ref = safeMetadata(ref, protected...)
	if len(task.Acceptance) == 0 {
		fmt.Fprintf(a.errorWriter(), "Warning: the task has no acceptance criteria; add them with: aircom task %s --workstream %s --acceptance <text>\n", ref, workstreamCode)
	}
	if strings.TrimSpace(task.Validation) == "" {
		fmt.Fprintf(a.errorWriter(), "Warning: the task has no validation; add it with: aircom task %s --workstream %s --validation <text>\n", ref, workstreamCode)
	}
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
		return taskCommentStatusError(response.status, response.body, workstreamCode, protectedTaskID, credential)
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

func (a *App) setTaskStatus(workstreamCode string, taskID string, change taskStatusRequest, credential credentials.Credential) error {
	status := change.Status
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task status idempotency ID."}
	}
	change.IdempotencyID = idempotencyID
	payload, err := json.Marshal(change)
	if err != nil {
		return &publicError{message: "Unable to prepare the task status change."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/tasks/" + url.PathEscape(taskID)
	response, err := a.messageAPIRequest(http.MethodPatch, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		if fieldErr := taskFieldError(response.body); fieldErr != nil {
			return fieldErr
		}
		protectedTaskID := safeMetadata(taskID, credential.APIToken, credential.SocketKey)
		return taskStatusError(response.status, response.body, workstreamCode, protectedTaskID, credential)
	}
	updated, err := decodeTaskResponse(response.body)
	if err != nil || updated.ID != taskID || updated.Status != status {
		return &publicError{message: "The workstream service returned an invalid task status response."}
	}
	if _, err := io.WriteString(a.outputWriter(), formatTaskState(updated, nil, credential.APIToken, credential.SocketKey)); err != nil {
		return &publicError{message: "Unable to write task output."}
	}
	return nil
}

// setTaskAssignee hands a task to another agent in the workstream. The server
// records who handed it over and posts that as an update on the task.
func (a *App) setTaskAssignee(workstreamCode string, taskID string, assignee string, credential credentials.Credential) error {
	// One key per invocation, reused across its retries, so a retried handover
	// is applied once and posts one reassignment update.
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task reassignment idempotency ID."}
	}
	payload, err := json.Marshal(taskAssigneeRequest{Assignee: assignee, IdempotencyID: idempotencyID})
	if err != nil {
		return &publicError{message: "Unable to prepare the task reassignment."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/tasks/" + url.PathEscape(taskID)
	response, err := a.messageAPIRequest(http.MethodPatch, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		protectedTaskID := safeMetadata(taskID, credential.APIToken, credential.SocketKey)
		return taskAssigneeError(response.status, response.body, workstreamCode, protectedTaskID, credential)
	}
	updated, err := decodeTaskResponse(response.body)
	if err != nil || updated.ID != taskID {
		return &publicError{message: "The workstream service returned an invalid task reassignment response."}
	}
	if _, err := io.WriteString(a.outputWriter(), formatTaskState(updated, nil, credential.APIToken, credential.SocketKey)); err != nil {
		return &publicError{message: "Unable to write task output."}
	}
	return nil
}

// formatTaskState prints a task's fields; tasks, when given, names its
// dependencies and replacement by number, title and status.
func formatTaskState(task taskListItem, tasks []taskListItem, protected ...string) string {
	safe := func(value string) string { return safeMetadata(value, protected...) }
	orDash := func(value string) string {
		if value == "" {
			return "-"
		}
		return safe(value)
	}
	describe := func(id string) string {
		if other := findTaskByID(tasks, id); other != nil {
			return fmt.Sprintf("%s %s (%s)", taskLabel(*other), safe(other.Title), safe(other.Status))
		}
		return safe(id)
	}
	var output strings.Builder
	if task.Number > 0 {
		fmt.Fprintf(&output, "Number: #%d\n", task.Number)
	}
	fmt.Fprintf(&output, "Title: %s\n", safe(task.Title))
	fmt.Fprintf(&output, "Description: %s\n", orDash(task.Description))
	fmt.Fprintf(&output, "Status: %s\n", safe(task.Status))
	fmt.Fprintf(&output, "Assignee: %s\n", orDash(task.Assignee))
	fmt.Fprintf(&output, "Created: %s\n", safe(task.CreatedAt))
	fmt.Fprintf(&output, "Updated: %s\n", safe(task.UpdatedAt))
	if task.CreatedBy != nil {
		fmt.Fprintf(&output, "Created by: %s\n", safe(task.CreatedBy.Name))
	}
	if task.AssignedBy != nil && task.AssignedAt != "" {
		fmt.Fprintf(&output, "Assigned by: %s at %s\n", safe(task.AssignedBy.Name), safe(task.AssignedAt))
	}
	if task.Milestone != "" {
		fmt.Fprintf(&output, "Milestone: %s\n", safe(task.Milestone))
	}
	if task.Type != "" {
		fmt.Fprintf(&output, "Type: %s\n", safe(task.Type))
	}
	writeList := func(heading string, items []string, render func(string) string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&output, "%s:\n", heading)
		for _, item := range items {
			fmt.Fprintf(&output, "  - %s\n", render(item))
		}
	}
	writeList("Acceptance", task.Acceptance, safe)
	if task.Validation != "" {
		fmt.Fprintf(&output, "Validation: %s\n", safe(task.Validation))
	}
	writeList("Depends on", task.DependsOn, describe)
	writeList("Links", task.Links, safe)
	if task.CancelReason != "" {
		fmt.Fprintf(&output, "Cancelled: %s\n", safe(task.CancelReason))
		if task.CancelledBy != nil {
			fmt.Fprintf(&output, "Cancelled by: %s at %s\n", safe(task.CancelledBy.Name), safe(task.CancelledAt))
		}
		if task.ReplacedBy != "" {
			fmt.Fprintf(&output, "Replaced by: %s\n", describe(task.ReplacedBy))
		}
	}
	return output.String()
}

func findTaskByID(tasks []taskListItem, id string) *taskListItem {
	for index := range tasks {
		if tasks[index].ID == id {
			return &tasks[index]
		}
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
	var status, milestone, taskType string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.BoolVar(&mine, "mine", false, "show only tasks assigned to the selected agent")
	flags.StringVar(&status, "status", "", "task status")
	flags.StringVar(&milestone, "milestone", "", "show only tasks in this milestone")
	flags.StringVar(&taskType, "type", "", "show only tasks of this type (Other: no type)")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: tasksUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if status != "" && !validTaskListStatus(status) {
		return &publicError{message: taskStatusChoices}
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
		return workstreamResponseStatusError(response.status, response.body, workstreamCode, false, credential)
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
		if milestone != "" && !strings.EqualFold(task.Milestone, strings.TrimSpace(milestone)) {
			continue
		}
		if taskType != "" && !strings.EqualFold(displayTaskType(task.Type), strings.TrimSpace(taskType)) {
			continue
		}
		matches++
		orDash := func(value string) string {
			if value == "" {
				return "-"
			}
			return value
		}
		number := "-"
		if task.Number > 0 {
			number = "#" + strconv.Itoa(task.Number)
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			number,
			safeMetadata(task.ID, protected...),
			safeMetadata(task.Status, protected...),
			safeMetadata(orDash(task.Assignee), protected...),
			safeMetadata(orDash(task.Milestone), protected...),
			safeMetadata(displayTaskType(task.Type), protected...),
			safeMetadata(task.Title, protected...),
		); err != nil {
			return &publicError{message: "Unable to write task output."}
		}
	}
	if matches == 0 {
		message := emptyTaskListMessage(workstreamCode, status, mine)
		if milestone != "" || taskType != "" {
			message = fmt.Sprintf("No tasks match those filters in workstream %s.", workstreamCode)
		}
		if _, err := fmt.Fprintln(writer, message); err != nil {
			return &publicError{message: "Unable to write task output."}
		}
	}
	return nil
}

// displayTaskType shows a task without a type as Other.
func displayTaskType(taskType string) string {
	if taskType == "" {
		return "Other"
	}
	return taskType
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

// agentsByWorkstream groups this machine's agents by the workstream they are
// in, keeping only those in organizationID. An agent the service shows in
// another organization is left out even if its code matches, and so is one
// with no organization recorded: it cannot be placed, so it is not claimed.
func agentsByWorkstream(agents []agentSummary, organizationID string) map[string][]localAgent {
	grouped := map[string][]localAgent{}
	for _, agent := range agents {
		code := strings.TrimSpace(agent.WorkstreamCode)
		if code == "" || agent.OrganizationID == "" || agent.OrganizationID != organizationID {
			continue
		}
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			name = agent.AgentID
		}
		grouped[code] = append(grouped[code], localAgent{ID: agent.AgentID, Name: name})
	}
	for code := range grouped {
		sort.Slice(grouped[code], func(i, j int) bool { return grouped[code][i].Name < grouped[code][j].Name })
	}
	return grouped
}

// localAgent is one agent on this machine, as a workstream listing names it.
type localAgent struct {
	ID   string
	Name string
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
	organizationReference = strings.TrimSpace(organizationReference)
	// With neither, join goes wherever a human has sent this agent from the
	// dashboard. Naming only one of them is a mistake rather than a request.
	pickUp := workstreamCode == "" && organizationReference == ""
	if !pickUp && (workstreamCode == "" || organizationReference == "") {
		return &publicError{message: joinUsage}
	}
	if !pickUp {
		if err := validateWorkstreamCode(workstreamCode); err != nil {
			return err
		}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	agent, err := a.resolveAgent(agentReference)
	if err != nil {
		return err
	}
	// A joining listener claims the agent before it waits or joins, not only
	// once it starts listening: otherwise two sessions could both wait, both
	// join when the agent is sent, and the second would save its credential
	// over the first's.
	var held *agentlock.Lock
	if listen {
		held, err = a.claimAgent(agent.AgentID)
		if err != nil {
			return err
		}
		defer func() { _ = held.Release() }()
	}
	var organizationID string
	if pickUp {
		agent, err = a.awaitAssignment(agent, listen)
		if err != nil {
			return err
		}
		if strings.TrimSpace(agent.WorkstreamCode) != "" {
			// Already in one — the assignment was completed earlier, or the
			// agent was joined some other way. Hand the identity back.
			workstreamCode = agent.WorkstreamCode
			organizationID = agent.OrganizationID
		} else {
			organizationID = agent.AssignedOrganizationID
			workstreamCode = agent.AssignedWorkstreamCode
		}
	} else {
		organizationID, err = a.resolveOrganization(organizationReference)
		if err != nil {
			return err
		}
	}
	// Codes repeat across organizations, so "already there" means the same
	// organization and code as the service records for the agent. An agent with
	// no organization recorded is never assumed to be where it was asked to go.
	if strings.TrimSpace(agent.WorkstreamCode) == workstreamCode && agent.OrganizationID != "" && agent.OrganizationID == organizationID {
		// A dashboard Stop leaves this machine-level binding in place while
		// revoking its agent bearer. Do not hand a restarted runtime a dead
		// credential: prove the locally stored bearer still works first.
		credential, err := a.Store.FindByAgent(workstreamCode, agent.AgentID)
		if err != nil {
			if legacy := legacyStorageError(err); legacy != nil {
				return legacy
			}
			return &publicError{message: fmt.Sprintf(
				"%s is already in workstream %s, but its stored credential cannot be verified. To recover, run:\n    aircom leave --agent %s\n    aircom join --agent %s --org %s --workstream %s",
				agent.Name, workstreamCode, agent.AgentID, agent.AgentID, organizationID, workstreamCode)}
		}
		response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+workstreamCode, credential.APIToken, nil)
		if err != nil {
			return err
		}
		if response.status == http.StatusUnauthorized {
			return revokedSessionRecoveryError(response.body, credential)
		}
		if response.status < 200 || response.status >= 300 {
			return workstreamResponseStatusError(response.status, response.body, workstreamCode, false, credential)
		}
		// Already there. Re-running join is how a restarted runtime asks for
		// its agent back, so report the identity only after its bearer is live.
		a.reportAgentIdentity(listen, agent.AgentID, agent.Name, workstreamCode, socketAddressForAgentID(agent.AgentID))
		if listen {
			return a.listenAs(workstreamCode, agent.AgentID, held)
		}
		return nil
	}
	if strings.TrimSpace(agent.WorkstreamCode) != "" {
		if strings.TrimSpace(agent.WorkstreamCode) == workstreamCode {
			return &publicError{message: fmt.Sprintf(
				"%s is already in workstream %s in another organization. Take it out first:\n    aircom leave --agent %s",
				agent.Name, agent.WorkstreamCode, agent.Name)}
		}
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
		return joinLifecycleError(response.status, response.body)
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
		OrganizationID: organizationID,
	}); err != nil {
		return &publicError{message: "Joined the workstream but could not store the agent credential."}
	}

	a.reportAgentIdentity(listen, joined.AgentID, joined.AgentName, joined.WorkstreamCode, joined.SocketAddress)
	if listen {
		return a.listenAs(joined.WorkstreamCode, joined.AgentID, held)
	}
	return nil
}

// assignmentPollInterval is how often a waiting agent checks whether it has
// been sent somewhere. The dashboard shows the agent as waiting meanwhile.
const assignmentPollInterval = 5 * time.Second

// awaitAssignment returns the agent once it has somewhere to go. Under --listen
// it waits for a human to send it from the dashboard, which is what lets a
// click there take effect without anyone typing a command; without --listen
// there is nothing to wait in, so it says how to proceed instead.
func (a *App) awaitAssignment(agent agentSummary, listen bool) (agentSummary, error) {
	if agentHasSomewhereToBe(agent) {
		return agent, nil
	}
	if !listen {
		return agentSummary{}, &publicError{message: fmt.Sprintf(
			"%s has not been sent to a workstream. Send it from the dashboard's account page, or name one:\n    aircom join --agent %s --org <org> --workstream <code>",
			agent.Name, agent.Name)}
	}
	// Standard output is the wake-line stream under --listen, so this goes to
	// standard error like the identity block does.
	fmt.Fprintf(a.errorWriter(), "Waiting for %s to be sent to a workstream from the dashboard.\n", agent.Name)
	for poll := 0; !a.listenLimitReached(poll); poll++ {
		a.sleepForListen(assignmentPollInterval)
		latest, err := a.resolveAgent(agent.AgentID)
		if err != nil {
			return agentSummary{}, err
		}
		if agentHasSomewhereToBe(latest) {
			return latest, nil
		}
	}
	return agentSummary{}, &publicError{message: fmt.Sprintf("%s was not sent to a workstream.", agent.Name)}
}

func agentHasSomewhereToBe(agent agentSummary) bool {
	return strings.TrimSpace(agent.WorkstreamCode) != "" ||
		(strings.TrimSpace(agent.AssignedWorkstreamCode) != "" && strings.TrimSpace(agent.AssignedOrganizationID) != "")
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
