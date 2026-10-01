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
)

const reviewUsage = "Usage: aircom review start <task> --of <task> [--commits <a>..<b>] [--repo <path>] | review finding <task> --severity <critical|major|minor|nit> --category <correctness|security|performance|tests|style|docs|design|other> --summary <text> [--file <path>] [--line <n>] | review finish <task> --outcome <approved|sent_back> [--no-findings] | review finding-status <findingId> --status <fixed|wontfix|invalid>; all forms require --workstream <code> [--agent <agentId|name>]"

func (a *App) review(args []string) error {
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return &publicError{message: reviewUsage}
	}
	action, ref := args[0], args[1]
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, of, commits, repo, severity, category, summary, file, outcome, status string
	var line int
	var noFindings bool
	flags.StringVar(&code, "workstream", "", "workstream")
	flags.StringVar(&agent, "agent", "", "agent")
	flags.StringVar(&of, "of", "", "reviewed task")
	flags.StringVar(&commits, "commits", "", "commit range")
	flags.StringVar(&repo, "repo", ".", "local repository")
	flags.StringVar(&severity, "severity", "", "severity")
	flags.StringVar(&category, "category", "", "category")
	flags.StringVar(&summary, "summary", "", "finding summary")
	flags.StringVar(&file, "file", "", "file")
	flags.IntVar(&line, "line", 0, "line")
	flags.StringVar(&outcome, "outcome", "", "review outcome")
	flags.BoolVar(&noFindings, "no-findings", false, "explicit zero findings")
	flags.StringVar(&status, "status", "", "finding resolution")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 || code == "" {
		return &publicError{message: reviewUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	switch action {
	case "start":
		if of == "" || severity != "" || outcome != "" || status != "" {
			return &publicError{message: reviewUsage}
		}
	case "finding":
		if !map[string]bool{"critical": true, "major": true, "minor": true, "nit": true}[severity] || !map[string]bool{"correctness": true, "security": true, "performance": true, "tests": true, "style": true, "docs": true, "design": true, "other": true}[category] || strings.TrimSpace(summary) == "" || line < 0 {
			return &publicError{message: reviewUsage}
		}
	case "finish":
		if outcome != "approved" && outcome != "sent_back" {
			return &publicError{message: reviewUsage}
		}
	case "finding-status":
		if status != "fixed" && status != "wontfix" && status != "invalid" {
			return &publicError{message: reviewUsage}
		}
	default:
		return &publicError{message: reviewUsage}
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	id := ref
	if action != "finding-status" {
		id, err = a.resolveTaskID(code, ref, credential)
		if err != nil {
			return err
		}
	}
	var payload any
	var method = http.MethodPost
	var path string
	switch action {
	case "start":
		size := map[string]any{"additions": 0, "deletions": 0, "filesChanged": 0}
		if commits != "" {
			if strings.Count(commits, "..") != 1 || strings.Contains(commits, "...") {
				return &publicError{message: "--commits must be a range a..b."}
			}
			output, e := gitOutput(repo, "diff", "--numstat", "--no-renames", commits)
			if e != nil {
				return e
			}
			for _, row := range strings.Split(output, "\n") {
				if row == "" {
					continue
				}
				parts := strings.SplitN(row, "\t", 3)
				if len(parts) != 3 {
					return &publicError{message: "Git returned invalid numstat."}
				}
				size["filesChanged"] = size["filesChanged"].(int) + 1
				if parts[0] != "-" {
					v, e := strconv.Atoi(parts[0])
					if e != nil {
						return &publicError{message: "Git returned invalid numstat."}
					}
					size["additions"] = size["additions"].(int) + v
				}
				if parts[1] != "-" {
					v, e := strconv.Atoi(parts[1])
					if e != nil {
						return &publicError{message: "Git returned invalid numstat."}
					}
					size["deletions"] = size["deletions"].(int) + v
				}
			}
		}
		payload = map[string]any{"of": of, "commits": commits, "size": size}
		path = "reviews/" + url.PathEscape(id) + "/start"
	case "finding":
		payload = map[string]any{"severity": severity, "category": category, "summary": summary, "file": file, "line": line}
		path = "reviews/" + url.PathEscape(id) + "/findings"
	case "finish":
		payload = map[string]any{"outcome": outcome, "noFindings": noFindings}
		path = "reviews/" + url.PathEscape(id) + "/finish"
	case "finding-status":
		payload = map[string]any{"status": status}
		path = "findings/" + url.PathEscape(id)
		method = http.MethodPatch
	}
	data, e := json.Marshal(payload)
	if e != nil {
		return &publicError{message: "Could not encode review request."}
	}
	response, e := a.request(method, "/agent/v1/workstreams/"+code+"/"+path, credential.APIToken, data)
	if e != nil {
		return e
	}
	if response.status < 200 || response.status >= 300 {
		return reviewResponseError(action, response.status, response.body, code, credential)
	}
	_, e = fmt.Fprintf(a.outputWriter(), "Review %s recorded for %s.\n", action, safeMetadata(ref, credential.APIToken, credential.SocketKey))
	return e
}

// reviewRecordingHint explains the shape the server requires; agents that ran
// review start on the task under review got a bare "workstream not found".
const reviewRecordingHint = "A review is recorded on its own task of type review, assigned to you; " +
	"name the task being reviewed with --of. Create one with: aircom task create --type review --assignee <you> --title <text>."

// reviewResponseError shows the server's reason for review failures. The
// generic workstream mapping turned "Review or task not found" into
// "Workstream N was not found", which hid the real problem.
func reviewResponseError(action string, status int, body []byte, code string, credential credentials.Credential) error {
	if status == http.StatusUnauthorized || status == http.StatusConflict {
		return workstreamResponseStatusError(status, body, code, true, credential)
	}
	var response struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &response) != nil || strings.TrimSpace(response.Message) == "" {
		return workstreamResponseStatusError(status, body, code, true, credential)
	}
	message := fmt.Sprintf("Review %s failed: %s (HTTP %d).", action,
		safeMetadata(strings.TrimSpace(response.Message), credential.APIToken, credential.SocketKey), status)
	if action != "finding-status" && (status == http.StatusBadRequest || status == http.StatusNotFound) {
		message += " " + reviewRecordingHint
	}
	return &publicError{message: message}
}
