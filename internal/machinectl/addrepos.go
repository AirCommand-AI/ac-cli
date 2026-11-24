package machinectl

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// CapabilityAddRepos tells the dashboard that this daemon clones
// repositories added to an existing agent without restarting it.
const CapabilityAddRepos = "add-repos"

// RepoTokens renews the run's GitHub tokens so a newly added repository's
// owner is covered before cloning.
type RepoTokens interface {
	For(ctx context.Context, owner string, force bool) (string, error)
}

// RepoReporter receives the result of cloning one added repository.
type RepoReporter interface {
	ReportRepo(ctx context.Context, agentID, repo string, ok bool, reason string) error
}

// addedRepos lists server repositories missing from the saved definition.
func addedRepos(saved, server []string) []string {
	var added []string
	for _, repo := range server {
		known := false
		for _, s := range saved {
			if strings.EqualFold(s, repo) {
				known = true
				break
			}
		}
		if !known {
			added = append(added, repo)
		}
	}
	return added
}

// addRepos clones repositories added on the server to an existing agent's
// work folder. The running agent is not restarted and no revision is needed.
// A repository is saved only after it is cloned, so a failure is retried on
// the next reconcile. Repositories removed on the server are left in place.
// It returns the repositories now saved for the agent.
func (r *AgentReconciler) addRepos(ctx context.Context, target Agent, local supervisor.AgentDefinition, renewed map[string]bool) ([]string, []string) {
	saved := local.Repos
	added := addedRepos(local.Repos, target.Repos)
	if len(added) == 0 {
		return saved, nil
	}
	saved = append([]string(nil), saved...)
	var failures []string
	for _, repo := range added {
		err := r.addRepo(ctx, local, repo, renewed)
		r.reportRepo(ctx, local.AgentID, repo, err)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: add repository %s: %v", local.AgentID, repo, err))
			continue
		}
		saved = append(saved, repo)
	}
	return saved, failures
}

func (r *AgentReconciler) addRepo(ctx context.Context, local supervisor.AgentDefinition, repo string, renewed map[string]bool) error {
	if err := validRepo(repo); err != nil {
		return err
	}
	folder, err := workFolder(r.Home, local.WorkFolder)
	if err != nil {
		return err
	}
	if err := r.renewToken(ctx, repo, renewed); err != nil {
		return err
	}
	if err := cloneRepo(ctx, r.Home, folder, local.Name, repo); err != nil {
		return err
	}
	if _, err := r.Manager.AddRepo(local.Name, repo); err != nil {
		return fmt.Errorf("save repository: %w", err)
	}
	return nil
}

// renewToken force-renews the run's GitHub tokens once per owner per
// reconcile: the server mints tokens limited to the run's repositories, so
// the cached token does not cover a repository added since.
func (r *AgentReconciler) renewToken(ctx context.Context, repo string, renewed map[string]bool) error {
	if r.Tokens == nil {
		return nil
	}
	owner, _, _ := strings.Cut(repo, "/")
	key := strings.ToLower(owner)
	if renewed[key] {
		return nil
	}
	if _, err := r.Tokens.For(ctx, owner, true); err != nil {
		return fmt.Errorf("renew GitHub token for %s: %w", owner, err)
	}
	renewed[key] = true
	return nil
}

// reportRepo sends a success once and each distinct failure once per agent
// and repository. A failed report is logged and never blocks cloning.
func (r *AgentReconciler) reportRepo(ctx context.Context, agentID, repo string, cloneErr error) {
	if r.RepoReports == nil {
		return
	}
	reason := ""
	if cloneErr != nil {
		reason = shortReason(cloneErr.Error())
	}
	key := agentID + "\x00" + strings.ToLower(repo)
	if last, seen := r.repoReported[key]; seen && last == reason {
		return
	}
	if r.repoReported == nil {
		r.repoReported = make(map[string]string)
	}
	r.repoReported[key] = reason
	if err := r.RepoReports.ReportRepo(ctx, agentID, repo, cloneErr == nil, reason); err != nil {
		log.Printf("machine reconcile: report repository %s for %s: %v", repo, agentID, err)
	}
}
