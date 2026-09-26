package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

// Structured task fields (ac-dashboard#24): task numbers, milestone, type,
// acceptance criteria, validation, dependencies, links, and cancellation.

// maxTaskNumber matches the service: larger numbers would be read as IDs.
const maxTaskNumber = 999_999_999

// taskIDLength is the length of every task ID the service assigns.
const taskIDLength = 16

// stringList is a repeatable flag. Blank values are dropped, so passing the
// flag once with an empty value clears the list.
type stringList struct {
	values []string
}

func (l *stringList) String() string { return strings.Join(l.values, ",") }

func (l *stringList) Set(value string) error {
	if value = strings.TrimSpace(value); value != "" {
		l.values = append(l.values, value)
	}
	return nil
}

// list returns the values, never nil, so an explicit clear is sent as [].
func (l *stringList) list() []string {
	if l.values == nil {
		return []string{}
	}
	return l.values
}

// taskFieldFlags are the structured fields that create and edit share.
type taskFieldFlags struct {
	milestone  string
	taskType   string
	validation string
	acceptance stringList
	dependsOn  stringList
	links      stringList
}

func (f *taskFieldFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&f.milestone, "milestone", "", "milestone label")
	flags.StringVar(&f.taskType, "type", "", "task type label")
	flags.StringVar(&f.validation, "validation", "", "commands or evidence expected")
	flags.Var(&f.acceptance, "acceptance", "an acceptance criterion (repeatable)")
	flags.Var(&f.dependsOn, "depends-on", "a task ID or number this task waits on (repeatable)")
	flags.Var(&f.links, "link", "an http or https link (repeatable)")
}

// taskFieldNames are the flags taskFieldFlags registers.
var taskFieldNames = map[string]bool{
	"milestone": true, "type": true, "validation": true, "acceptance": true, "depends-on": true, "link": true,
}

// editRequest is the edit the given flags ask for, keyed by the flags set on
// the command line; only those fields are sent.
func (f *taskFieldFlags) editRequest(set map[string]bool) taskEditRequest {
	var edit taskEditRequest
	if set["milestone"] {
		edit.Milestone = &f.milestone
	}
	if set["type"] {
		edit.Type = &f.taskType
	}
	if set["validation"] {
		edit.Validation = &f.validation
	}
	if set["acceptance"] {
		values := f.acceptance.list()
		edit.Acceptance = &values
	}
	if set["depends-on"] {
		values := f.dependsOn.list()
		edit.DependsOn = &values
	}
	if set["link"] {
		values := f.links.list()
		edit.Links = &values
	}
	return edit
}

// parseTaskNumber reads a task reference as a number: "#17", or plain digits
// shorter than a task ID. The service applies the same rule.
func parseTaskNumber(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	digits := strings.TrimPrefix(ref, "#")
	if digits == "" || (digits == ref && len(ref) >= taskIDLength) {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || n > maxTaskNumber || strconv.Itoa(n) != digits {
		return 0, false
	}
	return n, true
}

// taskLabel names a task as #n when it has a number, else by its ID.
func taskLabel(task taskListItem) string {
	if task.Number > 0 {
		return fmt.Sprintf("#%d", task.Number)
	}
	return task.ID
}

// readTaskDetail fetches the workstream's tasks and updates.
func (a *App) readTaskDetail(workstreamCode string, credential credentials.Credential) (taskListEnvelope, error) {
	response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+workstreamCode, credential.APIToken, nil)
	if err != nil {
		return taskListEnvelope{}, err
	}
	if response.status < 200 || response.status >= 300 {
		return taskListEnvelope{}, workstreamResponseStatusError(response.status, response.body, workstreamCode, false, credential)
	}
	detail, err := decodeTaskDetail(response.body)
	if err != nil {
		return taskListEnvelope{}, &publicError{message: "The workstream service returned an invalid task response."}
	}
	return detail, nil
}

// findTask picks the task a reference names: by number when it reads as one,
// else by ID.
func findTask(tasks []taskListItem, ref string) *taskListItem {
	number, isNumber := parseTaskNumber(ref)
	for index := range tasks {
		if isNumber && tasks[index].Number == number || !isNumber && tasks[index].ID == ref {
			return &tasks[index]
		}
	}
	return nil
}

// resolveTaskID turns a task number into the task's ID, reading the
// workstream; an ID is returned as it is.
func (a *App) resolveTaskID(workstreamCode string, ref string, credential credentials.Credential) (string, error) {
	if _, isNumber := parseTaskNumber(ref); !isNumber {
		return ref, nil
	}
	detail, err := a.readTaskDetail(workstreamCode, credential)
	if err != nil {
		return "", err
	}
	selected := findTask(detail.Tasks, ref)
	if selected == nil {
		return "", taskNotFoundError(ref, workstreamCode, credential)
	}
	return selected.ID, nil
}

func taskNotFoundError(ref string, workstreamCode string, credential credentials.Credential) error {
	protected := []string{credential.APIToken, credential.SocketKey}
	return &publicError{message: fmt.Sprintf(
		"Task %s was not found in workstream %s.",
		safeMetadata(ref, protected...),
		safeMetadata(workstreamCode, protected...),
	)}
}

// editTask changes a task's structured fields in one keyed request.
func (a *App) editTask(workstreamCode string, taskID string, edit taskEditRequest, credential credentials.Credential) error {
	idempotencyID, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return &publicError{message: "Unable to generate a task edit idempotency ID."}
	}
	edit.IdempotencyID = idempotencyID
	payload, err := json.Marshal(edit)
	if err != nil {
		return &publicError{message: "Unable to prepare the task edit."}
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/tasks/" + url.PathEscape(taskID)
	response, err := a.messageAPIRequest(http.MethodPatch, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		protectedTaskID := safeMetadata(taskID, credential.APIToken, credential.SocketKey)
		return taskEditError(response.status, response.body, workstreamCode, protectedTaskID)
	}
	updated, err := decodeTaskResponse(response.body)
	if err != nil || updated.ID != taskID {
		return &publicError{message: "The workstream service returned an invalid task edit response."}
	}
	if _, err := io.WriteString(a.outputWriter(), formatTaskState(updated, nil, credential.APIToken, credential.SocketKey)); err != nil {
		return &publicError{message: "Unable to write task output."}
	}
	return nil
}

// taskFieldError explains a refused structured field, or returns nil when the
// response is not one.
func taskFieldError(body []byte) error {
	response := serviceError(body)
	detail := singleLine(response.Message)
	switch response.Code {
	case "TaskNumberTaken":
		return &publicError{message: "That task number is already in use in this workstream."}
	case "TaskNumbersExhausted":
		return &publicError{message: "This workstream has used every task number."}
	case "TaskReferenceNotFound":
		return &publicError{message: "A task reference does not match a task in this workstream: " + detail}
	case "TaskSelfReference":
		return &publicError{message: "A task cannot depend on or be replaced by itself."}
	case "TaskDependencyCycle":
		return &publicError{message: "Those dependencies would form a loop."}
	case "TaskCancelReasonRequired":
		return &publicError{message: "Cancelling a task requires --reason."}
	case "TaskAlreadyCancelled":
		return &publicError{message: "The task is already cancelled."}
	case "TaskReopenForbidden":
		return &publicError{message: "Only an agent can reopen a cancelled task."}
	case "InvalidTaskField":
		return &publicError{message: "AirCommand rejected a task field: " + detail}
	}
	return nil
}

func taskEditError(status int, body []byte, workstreamCode string, taskID string) error {
	if err := taskFieldError(body); err != nil {
		return err
	}
	switch status {
	case http.StatusUnauthorized:
		return &publicError{message: fmt.Sprintf("You were stopped or removed from workstream %s.", workstreamCode)}
	case http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Task %s was not found in workstream %s.", singleLine(taskID), workstreamCode)}
	case http.StatusConflict:
		if responseCode(body) == "WorkstreamPaused" {
			return &publicError{message: fmt.Sprintf("Workstream %s is paused; task edit rejected.", workstreamCode)}
		}
		return &publicError{message: "AirCommand rejected the task edit because of a conflict (HTTP 409)."}
	default:
		return &publicError{message: fmt.Sprintf("AirCommand task edit failed (HTTP %d).", status)}
	}
}
