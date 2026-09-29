package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

type commitEvidence struct {
	Repo         string   `json:"repo"`
	SHA          string   `json:"sha"`
	Author       string   `json:"author"`
	CommittedAt  string   `json:"committedAt"`
	Additions    int      `json:"additions"`
	Deletions    int      `json:"deletions"`
	FilesChanged int      `json:"filesChanged"`
	Files        []string `json:"files"`
}

func gitOutput(repo string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", &publicError{message: "Could not read commit from local git repository."}
	}
	return strings.TrimSpace(string(output)), nil
}
func gitRepoName(repo string) (string, error) {
	remote, err := gitOutput(repo, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	remote = strings.TrimSuffix(strings.TrimSuffix(remote, ".git"), "/")
	if idx := strings.Index(remote, "github.com/"); idx >= 0 {
		remote = remote[idx+len("github.com/"):]
	} else if idx := strings.Index(remote, "github.com:"); idx >= 0 {
		remote = remote[idx+len("github.com:"):]
	} else {
		return "", &publicError{message: "Local origin must point at a GitHub owner/repo."}
	}
	if strings.Count(remote, "/") != 1 || strings.ContainsAny(remote, " \r\n") {
		return "", &publicError{message: "Local origin must point at a GitHub owner/repo."}
	}
	return remote, nil
}

var shaReference = regexp.MustCompile(`^[a-fA-F0-9]{7,40}$`)

func localCommit(repo, repoName, ref string) (commitEvidence, error) {
	if !shaReference.MatchString(ref) {
		return commitEvidence{}, &publicError{message: "--commit requires a git SHA (7–40 hexadecimal characters)."}
	}
	// rev-parse --verify ref^{commit} rejects missing/non-commit objects; git
	// show then computes the numbers rather than accepting agent-typed totals.
	sha, err := gitOutput(repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return commitEvidence{}, err
	}
	author, err := gitOutput(repo, "show", "-s", "--format=%an <%ae>", sha)
	if err != nil {
		return commitEvidence{}, err
	}
	committedAt, err := gitOutput(repo, "show", "-s", "--format=%cI", sha)
	if err != nil {
		return commitEvidence{}, err
	}
	if _, err = time.Parse(time.RFC3339, committedAt); err != nil {
		return commitEvidence{}, &publicError{message: "Git returned an invalid commit timestamp."}
	}
	numstat, err := gitOutput(repo, "show", "--format=", "--numstat", "--no-renames", sha)
	if err != nil {
		return commitEvidence{}, err
	}
	result := commitEvidence{Repo: repoName, SHA: sha, Author: author, CommittedAt: committedAt, Files: []string{}}
	for _, line := range strings.Split(numstat, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return commitEvidence{}, &publicError{message: "Git returned invalid numstat output."}
		}
		if parts[0] != "-" {
			n, e := strconv.Atoi(parts[0])
			if e != nil {
				return commitEvidence{}, &publicError{message: "Git returned invalid additions."}
			}
			result.Additions += n
		}
		if parts[1] != "-" {
			n, e := strconv.Atoi(parts[1])
			if e != nil {
				return commitEvidence{}, &publicError{message: "Git returned invalid deletions."}
			}
			result.Deletions += n
		}
		result.Files = append(result.Files, parts[2])
		result.FilesChanged++
	}
	return result, nil
}
func (a *App) addTaskCommits(code, id string, refs []string, commitRange, repo string, credential credentials.Credential) error {
	if len(refs) == 0 && commitRange == "" {
		return &publicError{message: "Provide --commit <sha> or --commits <a>..<b>."}
	}
	if repo == "" {
		repo = "."
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return &publicError{message: "Invalid repository path."}
	}
	name, err := gitRepoName(repo)
	if err != nil {
		return err
	}
	if commitRange != "" {
		if strings.Count(commitRange, "..") != 1 || strings.Contains(commitRange, "...") {
			return &publicError{message: "--commits must be a range a..b."}
		}
		output, e := gitOutput(repo, "rev-list", "--reverse", commitRange)
		if e != nil {
			return e
		}
		if output == "" {
			return &publicError{message: "No commits in the supplied range."}
		}
		refs = strings.Split(output, "\n")
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		evidence, e := localCommit(repo, name, ref)
		if e != nil {
			return e
		}
		if seen[evidence.SHA] {
			continue
		}
		seen[evidence.SHA] = true
		payload, e := json.Marshal(evidence)
		if e != nil {
			return &publicError{message: "Could not encode commit evidence."}
		}
		response, e := a.request(http.MethodPost, "/agent/v1/workstreams/"+code+"/tasks/"+url.PathEscape(id)+"/commits", credential.APIToken, payload)
		if e != nil {
			return e
		}
		if response.status < 200 || response.status >= 300 {
			return workstreamResponseStatusError(response.status, response.body, code, false, credential)
		}
		_, e = fmt.Fprintf(a.outputWriter(), "Recorded commit %s for task %s.\n", evidence.SHA, safeMetadata(id, credential.APIToken, credential.SocketKey))
		if e != nil {
			return &publicError{message: "Unable to write commit confirmation."}
		}
	}
	return nil
}
func (a *App) reportTaskTests(code, id, value, suite string, credential credentials.Credential) error {
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return &publicError{message: "--tests must be passed/failed[/skipped]."}
	}
	counts := []int{0, 0, 0}
	for i, part := range parts {
		n, e := strconv.Atoi(part)
		if e != nil || n < 0 {
			return &publicError{message: "--tests counts must be nonnegative integers."}
		}
		counts[i] = n
	}
	payload, e := json.Marshal(map[string]any{"passed": counts[0], "failed": counts[1], "skipped": counts[2], "suite": suite})
	if e != nil {
		return &publicError{message: "Could not encode test results."}
	}
	response, e := a.request(http.MethodPost, "/agent/v1/workstreams/"+code+"/tasks/"+url.PathEscape(id)+"/tests", credential.APIToken, payload)
	if e != nil {
		return e
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	_, e = fmt.Fprintf(a.outputWriter(), "Recorded tests for task %s: %s.\n", safeMetadata(id, credential.APIToken, credential.SocketKey), value)
	return e
}
