package machinectl

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/enroll"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// Manager's individual lifecycle operations acquire their own lock. Reconcile
// performs all HTTP, joins and clones without holding it.
type Manager interface {
	Definitions() []supervisor.AgentDefinition
	Start(context.Context, supervisor.AgentDefinition) error
	Stage(supervisor.AgentDefinition) error
	Stop(context.Context, string) error
	Mode(context.Context, string, string) error
	MarkRevision(string, int64) error
	FlushDesired(context.Context) map[string]bool
}

type AgentReconciler struct {
	API     API
	Manager Manager
	Store   *credentials.Store
	Home    string
	// Join verifies/resumes a bearer. Tests replace it with a fake.
	Join         func(context.Context, Agent) error
	Gate         *sync.Mutex
	Now          func() time.Time
	failed       map[string]retryFailure
	seedRejected map[string]bool
}

type retryFailure struct {
	revision int64
	until    time.Time
	message  string
}

func (r *AgentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *AgentReconciler) Reconcile(ctx context.Context) error {
	if r.API == nil || r.Manager == nil || r.Store == nil {
		return fmt.Errorf("reconciler is not configured")
	}
	if r.Gate != nil {
		r.Gate.Lock()
		defer r.Gate.Unlock()
	}
	// A failed local desired post must be retried before fetching an older
	// server revision that could undo the local operator's successful action.
	skip := r.Manager.FlushDesired(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	local := r.Manager.Definitions()
	remote, err := r.API.Definitions(ctx)
	if err != nil {
		return err
	}
	var failures []string
	present := make(map[string]bool, len(remote.Agents))
	for _, d := range remote.Agents {
		present[d.AgentID] = true
	}
	seeded := false
	// Seed one agent per request. A missing credential or retired roster row
	// must not strand every other agent. Each tick also sees locally created
	// agents not present at the previous check-in.
	for _, d := range local {
		if present[d.AgentID] || r.seedRejected[d.AgentID] {
			continue
		}
		cred, err := r.Store.FindByAgent(d.Workstream, d.AgentID)
		if err == nil && cred.OrganizationID == "" {
			err = fmt.Errorf("credential has no workspace ID")
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("seed %s: %v", d.Name, err))
			continue
		}
		seed := Seed{AgentID: d.AgentID, Desired: d.Desired, Mode: d.Mode, Repos: d.Repos, WorkFolder: d.WorkFolder, AssignedOrganizationID: cred.OrganizationID, AssignedWorkstreamCode: d.Workstream}
		if err := r.API.Seed(ctx, []Seed{seed}); err != nil {
			var status *HTTPStatusError
			var result *SeedResultError
			permanent := errors.As(err, &result) && result.AgentID == d.AgentID && result.Permanent()
			if errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusConflict) {
				permanent = true
			}
			if permanent {
				if r.seedRejected == nil {
					r.seedRejected = make(map[string]bool)
				}
				r.seedRejected[d.AgentID] = true
			}
			failures = append(failures, fmt.Sprintf("seed %s: %v", d.Name, err))
			continue
		}
		seeded = true
	}
	if seeded {
		remote, err = r.API.Definitions(ctx)
		if err != nil {
			return err
		}
	}
	current := make(map[string]supervisor.AgentDefinition, len(local))
	for _, d := range local {
		current[d.AgentID] = d
	}
	for _, target := range remote.Agents {
		if attached, ok := r.Manager.(interface {
			Attached(string) (supervisor.AgentDefinition, bool)
		}); ok {
			if _, yes := attached.Attached(target.AgentID); yes {
				continue
			}
		}
		if skip[target.AgentID] {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		previous, exists := current[target.AgentID]
		if exists {
			target = r.withLocalDefaults(target, previous)
		}
		if target.Revision <= previous.Revision || target.Revision < 1 {
			continue
		}
		if failure, ok := r.failed[target.AgentID]; ok && failure.revision == target.Revision && r.now().Before(failure.until) {
			// A deferred retry is not a successful check: keep the failure on
			// the run card until this revision actually starts.
			failures = append(failures, fmt.Sprintf("%s: %s", target.AgentID, failure.message))
			continue
		}
		if err := r.apply(ctx, target, previous, exists); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target.AgentID, err))
			if r.failed == nil {
				r.failed = make(map[string]retryFailure)
			}
			r.failed[target.AgentID] = retryFailure{revision: target.Revision, until: r.now().Add(5 * time.Minute), message: err.Error()}
			if reportErr := r.API.Result(ctx, target.AgentID, target.Revision, "failed", shortReason(err.Error())); reportErr != nil {
				failures = append(failures, reportErr.Error())
			}
			continue
		}
		delete(r.failed, target.AgentID)
		// Do not persist the applied revision until the result was accepted;
		// a transient report failure is retried on the next check-in.
		if err := r.API.Result(ctx, target.AgentID, target.Revision, "applied", ""); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := r.Manager.MarkRevision(target.Name, target.Revision); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("reconcile: %s", strings.Join(failures, "; "))
	}
	return nil
}

func shortReason(reason string) string {
	if len(reason) <= 500 {
		return reason
	}
	reason = reason[:500]
	for !utf8.ValidString(reason) {
		reason = reason[:len(reason)-1]
	}
	return reason
}

// Legacy empty server fields preserve the local definition during seeding.
func (r *AgentReconciler) withLocalDefaults(target Agent, local supervisor.AgentDefinition) Agent {
	if target.Name == "" {
		target.Name = local.Name
	}
	if target.Desired == "" {
		target.Desired = local.Desired
	}
	if target.Mode == "" {
		target.Mode = local.Mode
	}
	if target.WorkFolder == "" {
		target.WorkFolder = local.WorkFolder
	}
	if target.Repos == nil {
		target.Repos = local.Repos
	}
	if target.AssignedWorkstreamCode == "" {
		target.AssignedWorkstreamCode = local.Workstream
	}
	if target.AssignedOrganizationID == "" {
		if cred, err := r.Store.FindByAgent(local.Workstream, local.AgentID); err == nil {
			target.AssignedOrganizationID = cred.OrganizationID
		}
	}
	return target
}

func (r *AgentReconciler) apply(ctx context.Context, target Agent, old supervisor.AgentDefinition, exists bool) error {
	if target.AgentID == "" || target.Name == "" || (target.Desired != "running" && target.Desired != "stopped") || (target.Mode != "headless" && target.Mode != "tmux") || target.AssignedOrganizationID == "" || target.AssignedWorkstreamCode == "" {
		return fmt.Errorf("incomplete or invalid server definition")
	}
	if exists && old.Name != target.Name {
		return fmt.Errorf("server changed agent name")
	}
	if exists && old.State == "taken-over" {
		return fmt.Errorf("agent is taken over")
	}
	if !exists {
		if err := prepareFolder(ctx, r.Home, target); err != nil {
			return err
		}
		if target.Desired == "stopped" {
			return r.Manager.Stage(definition(target))
		}
		if err := r.join(ctx, target); err != nil {
			return err
		}
		return r.Manager.Start(ctx, definition(target))
	}
	// Initial seeding never revives parked agents. Later explicit revisions
	// can resume a crashed or dashboard-stopped agent even if desired was
	// already running. Periodic checks without a newer revision did not reach
	// this method at all.
	unchanged := old.Desired == target.Desired && old.Mode == target.Mode && old.Workstream == target.AssignedWorkstreamCode && old.WorkFolder == target.WorkFolder
	if unchanged && (old.Revision == 0 || target.Desired != "running" || old.State == "running" || old.State == "starting") {
		return nil
	}
	if old.Desired == "running" && (target.Desired == "stopped" || target.Mode != old.Mode) {
		if err := r.Manager.Stop(ctx, old.Name); err != nil {
			return err
		}
	}
	if target.Mode != old.Mode {
		if err := r.Manager.Mode(ctx, old.Name, target.Mode); err != nil {
			return err
		}
	}
	if target.Desired == "running" {
		if old.Workstream != target.AssignedWorkstreamCode || old.WorkFolder != target.WorkFolder {
			return fmt.Errorf("moving an existing agent's workstream or folder requires a separate workflow")
		}
		if err := r.join(ctx, target); err != nil {
			return err
		}
		return r.Manager.Start(ctx, definition(target))
	}
	return nil
}

func definition(d Agent) supervisor.AgentDefinition {
	return supervisor.AgentDefinition{AgentID: d.AgentID, Name: d.Name, Organization: d.AssignedOrganizationID, Workstream: d.AssignedWorkstreamCode, WorkFolder: d.WorkFolder, Repos: d.Repos, Mode: d.Mode}
}

func (r *AgentReconciler) join(ctx context.Context, agent Agent) error {
	if r.Join != nil {
		return r.Join(ctx, agent)
	}
	return r.joinHTTP(ctx, agent)
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func prepareFolder(ctx context.Context, home string, agent Agent) error {
	root := filepath.Join(home, "work")
	path := filepath.Clean(agent.WorkFolder)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return fmt.Errorf("work folder must be below %s", root)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	for _, repo := range agent.Repos {
		if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
			return fmt.Errorf("invalid repository %q", repo)
		}
		dest := filepath.Join(path, strings.Split(repo, "/")[1])
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		// A failed clone never leaves a partial repository that a later check
		// might mistake for a completed checkout.
		tmp, err := os.MkdirTemp(path, ".aircom-clone-*")
		if err != nil {
			return err
		}
		timeout := 2 * time.Minute
		if _, runErr := os.Stat(filepath.Join(storagepath.Root(home), "run.json")); runErr == nil {
			timeout = 10 * time.Minute
		}
		cloneCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(cloneCtx, "git", "clone", "--", "https://github.com/"+repo+".git", tmp)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, cloneErr := cmd.CombinedOutput()
		cancel()
		if cloneErr != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("clone %s: %w: %s", repo, cloneErr, strings.TrimSpace(string(output)))
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		if _, runErr := os.Stat(filepath.Join(storagepath.Root(home), "run.json")); runErr == nil {
			for _, pair := range [][2]string{{"user.name", agent.Name + " (AirCommand)"}, {"user.email", "run-agents@users.noreply.aircommand.ai"}} {
				if out, e := exec.CommandContext(ctx, "git", "-C", dest, "config", "--local", pair[0], pair[1]).CombinedOutput(); e != nil {
					return fmt.Errorf("configure git author: %w: %s", e, strings.TrimSpace(string(out)))
				}
			}
		}
	}
	return nil
}

// JoinAssignment connects a locally attached, unplaced agent once a person
// assigns it. No daemon-started definition or desired-state post is created.
func (r *AgentReconciler) JoinAssignment(ctx context.Context, target Agent) error {
	if target.AgentID == "" || target.AssignedOrganizationID == "" || target.AssignedWorkstreamCode == "" {
		return fmt.Errorf("invalid attached assignment")
	}
	return r.joinHTTP(ctx, target)
}
func (r *AgentReconciler) joinHTTP(ctx context.Context, target Agent) error {
	api, ok := r.API.(HTTPAPI)
	if !ok {
		return fmt.Errorf("join transport is not configured")
	}
	machine, err := r.Store.LoadMachine()
	if err != nil {
		return err
	}
	if credential, err := r.Store.FindByAgent(target.AssignedWorkstreamCode, target.AgentID); err == nil && credential.OrganizationID == target.AssignedOrganizationID {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api.BaseURL, "/")+"/agent/v1/workstreams/"+url.PathEscape(target.AssignedWorkstreamCode), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+credential.APIToken)
		response, err := api.Client.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil
		}
		if response.StatusCode != http.StatusUnauthorized {
			return fmt.Errorf("verify agent bearer: HTTP %d", response.StatusCode)
		}
	}
	token, err := secrets.Credential(rand.Reader, "api_")
	if err != nil {
		return err
	}
	id, err := secrets.IdempotencyID(rand.Reader)
	if err != nil {
		return err
	}
	requestFn := func(method, path, bearer string, payload []byte) (enroll.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(api.BaseURL, "/")+path, strings.NewReader(string(payload)))
		if err != nil {
			return enroll.Response{}, err
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-AC-Organization", target.AssignedOrganizationID)
		req.Header.Set("Content-Type", "application/json")
		res, err := api.Client.Do(req)
		if err != nil {
			return enroll.Response{}, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return enroll.Response{Status: res.StatusCode, Body: body}, err
	}
	joined, err := enroll.Join(requestFn, machine.APIToken, target.AgentID, target.AssignedWorkstreamCode, token, id)
	if err != nil {
		return err
	}
	if joined.Status < 200 || joined.Status >= 300 {
		return fmt.Errorf("join agent: HTTP %d", joined.Status)
	}
	var identity enroll.Joined
	if err := json.Unmarshal(joined.Body, &identity); err != nil {
		return err
	}
	if identity.AgentID != target.AgentID || identity.WorkstreamCode != target.AssignedWorkstreamCode || identity.SocketAddress == "" {
		return fmt.Errorf("join returned a different or incomplete identity")
	}
	return r.Store.Save(credentials.Credential{APIToken: token, WorkstreamCode: identity.WorkstreamCode, AgentID: identity.AgentID, SocketAddress: identity.SocketAddress, AgentName: identity.AgentName, OrganizationID: target.AssignedOrganizationID})
}
