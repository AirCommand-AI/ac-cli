package machinectl

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/enroll"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
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
}

type AgentReconciler struct {
	API     API
	Manager Manager
	Store   *credentials.Store
	Home    string
	// Join verifies/resumes a bearer. Tests replace it with a fake.
	Join   func(context.Context, Agent) error
	seeded bool
}

func (r *AgentReconciler) Reconcile(ctx context.Context) error {
	if r.API == nil || r.Manager == nil || r.Store == nil {
		return fmt.Errorf("reconciler is not configured")
	}
	local := r.Manager.Definitions()
	if !r.seeded {
		seeds := make([]Seed, 0, len(local))
		for _, d := range local {
			cred, err := r.Store.FindByAgent(d.Workstream, d.AgentID)
			if err != nil {
				return fmt.Errorf("seed %s: %w", d.Name, err)
			}
			if cred.OrganizationID == "" {
				return fmt.Errorf("seed %s: missing organization ID in credential", d.Name)
			}
			seeds = append(seeds, Seed{AgentID: d.AgentID, Name: d.Name, Desired: d.Desired, Mode: d.Mode, Repos: d.Repos, WorkFolder: d.WorkFolder, AssignedOrganizationID: cred.OrganizationID, AssignedWorkstreamCode: d.Workstream})
		}
		if err := r.API.Seed(ctx, seeds); err != nil {
			return err
		}
		r.seeded = true
	}
	remote, err := r.API.Definitions(ctx)
	if err != nil {
		return err
	}
	current := make(map[string]supervisor.AgentDefinition, len(local))
	for _, d := range local {
		current[d.AgentID] = d
	}
	var failures []string
	for _, target := range remote.Agents {
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
		if err := r.apply(ctx, target, previous, exists); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target.AgentID, err))
			if reportErr := r.API.Result(ctx, target.AgentID, target.Revision, "failed", err.Error()); reportErr != nil {
				failures = append(failures, reportErr.Error())
			}
			continue
		}
		if err := r.Manager.MarkRevision(target.Name, target.Revision); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := r.API.Result(ctx, target.AgentID, target.Revision, "applied", ""); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("reconcile: %s", strings.Join(failures, "; "))
	}
	return nil
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
	// Seeding must not restart an already running agent. An unchanged revision
	// also leaves crashed, dashboard-stopped and takeover states untouched.
	if old.Desired == target.Desired && old.Mode == target.Mode && old.Workstream == target.AssignedWorkstreamCode && old.WorkFolder == target.WorkFolder {
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
		if _, err := os.Stat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		cloneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd := exec.CommandContext(cloneCtx, "git", "clone", "--", "https://github.com/"+repo+".git", dest)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("clone %s: %w: %s", repo, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
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
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api.BaseURL, "/")+"/agent/v1/workstreams/"+target.AssignedWorkstreamCode, nil)
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
