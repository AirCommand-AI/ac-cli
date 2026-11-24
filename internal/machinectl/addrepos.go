package machinectl

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// CapabilityAddRepos tells the dashboard that this daemon clones
// repositories added to an existing agent without restarting it.
const CapabilityAddRepos = "add-repos"

// A repository that keeps failing is retried after 30s, doubling up to 10 min.
const (
	repoBackoffBase = 30 * time.Second
	repoBackoffMax  = 10 * time.Minute
)

// RepoTokens renews the run's GitHub tokens so a newly added repository's
// owner is covered before cloning.
type RepoTokens interface {
	For(ctx context.Context, owner string, force bool) (string, error)
}

// RepoReporter receives the result of cloning one repository.
type RepoReporter interface {
	ReportRepo(ctx context.Context, agentID, repo string, ok bool, reason string) error
}

// repoState is the reconciler's in-memory record of added repositories.
type repoState struct {
	mu       sync.Mutex
	flushMu  sync.Mutex
	planned  []repoJob
	inFlight map[string]bool
	backoff  map[string]repoBackoff
	pending  map[string]repoReport
	acked    map[string]string
	jobs     sync.WaitGroup
}

type repoJob struct {
	agentID, name, folder, repo string
}

type repoBackoff struct {
	failures int
	until    time.Time
	message  string
}

type repoReport struct {
	agentID, repo string
	ok            bool
	reason        string
}

func (p repoReport) outcome() string { return fmt.Sprintf("%v:%s", p.ok, p.reason) }

func repoKey(agentID, repo string) string { return agentID + "\x00" + strings.ToLower(repo) }

// repoDelay is the wait after the given number of consecutive failures.
func repoDelay(failures int) time.Duration {
	delay := repoBackoffBase
	for i := 1; i < failures && delay < repoBackoffMax; i++ {
		delay *= 2
	}
	return min(delay, repoBackoffMax)
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

// planRepos picks repositories added on the server to an existing agent for
// cloning. It runs under the operation lock and only decides; the clone runs
// later without the lock. A repository already being cloned or backing off
// is skipped; a backing-off failure stays visible in the reconcile error.
func (r *AgentReconciler) planRepos(target Agent, local supervisor.AgentDefinition) []string {
	st := &r.repos
	st.mu.Lock()
	defer st.mu.Unlock()
	var failures []string
	for _, repo := range addedRepos(local.Repos, target.Repos) {
		key := repoKey(local.AgentID, repo)
		if st.inFlight[key] {
			continue
		}
		if b, ok := st.backoff[key]; ok && r.now().Before(b.until) {
			failures = append(failures, fmt.Sprintf("%s: add repository %s: %s", local.AgentID, repo, b.message))
			continue
		}
		if st.inFlight == nil {
			st.inFlight = make(map[string]bool)
		}
		st.inFlight[key] = true
		st.planned = append(st.planned, repoJob{agentID: local.AgentID, name: local.Name, folder: local.WorkFolder, repo: repo})
	}
	return failures
}

// startRepoJobs clones the planned repositories in the background so git
// never runs under the operation lock or delays the check loop.
func (r *AgentReconciler) startRepoJobs(ctx context.Context) {
	st := &r.repos
	st.mu.Lock()
	jobs := st.planned
	st.planned = nil
	st.mu.Unlock()
	if len(jobs) == 0 {
		return
	}
	st.jobs.Add(1)
	go func() {
		defer st.jobs.Done()
		renewed := make(map[string]bool)
		for _, job := range jobs {
			err := r.addRepo(ctx, job, renewed)
			if err != nil {
				log.Printf("machine reconcile: %s: add repository %s: %v", job.agentID, job.repo, err)
			}
			r.finishRepoJob(job, err)
		}
		r.flushRepoReports(ctx)
	}()
}

// waitRepoJobs waits for background clones; tests use it.
func (r *AgentReconciler) waitRepoJobs() { r.repos.jobs.Wait() }

func (r *AgentReconciler) addRepo(ctx context.Context, job repoJob, renewed map[string]bool) error {
	if err := validRepo(job.repo); err != nil {
		return err
	}
	folder, err := workFolder(r.Home, job.folder)
	if err != nil {
		return err
	}
	if err := r.renewToken(ctx, job.repo, renewed); err != nil {
		return err
	}
	if err := cloneRepo(ctx, r.Home, folder, job.name, job.repo); err != nil {
		return err
	}
	// Saving is brief; only this step takes the operation lock.
	if r.Gate != nil {
		r.Gate.Lock()
		defer r.Gate.Unlock()
	}
	if _, err := r.Manager.AddRepo(job.name, job.repo); err != nil {
		return fmt.Errorf("save repository: %w", err)
	}
	return nil
}

// renewToken force-renews the run's GitHub tokens once per owner per batch:
// the server mints tokens limited to the run's repositories, so the cached
// token does not cover a repository added since.
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

// finishRepoJob records a clone result: success clears the backoff, a
// failure extends it, and either outcome is queued for reporting.
func (r *AgentReconciler) finishRepoJob(job repoJob, err error) {
	st := &r.repos
	st.mu.Lock()
	defer st.mu.Unlock()
	key := repoKey(job.agentID, job.repo)
	delete(st.inFlight, key)
	if err == nil {
		delete(st.backoff, key)
	} else {
		if st.backoff == nil {
			st.backoff = make(map[string]repoBackoff)
		}
		b := st.backoff[key]
		b.failures++
		b.until = r.now().Add(repoDelay(b.failures))
		b.message = err.Error()
		st.backoff[key] = b
	}
	r.queueReportLocked(job.agentID, job.repo, err)
}

// queueInitialRepos reports the repositories cloned when an agent was
// created, so the dashboard does not wait on them.
func (r *AgentReconciler) queueInitialRepos(target Agent) {
	if r.RepoReports == nil {
		return
	}
	r.repos.mu.Lock()
	defer r.repos.mu.Unlock()
	for _, repo := range target.Repos {
		r.queueReportLocked(target.AgentID, repo, nil)
	}
}

// queueReportLocked queues an outcome unless the server already
// acknowledged the same one. Callers hold repos.mu.
func (r *AgentReconciler) queueReportLocked(agentID, repo string, cloneErr error) {
	if r.RepoReports == nil {
		return
	}
	report := repoReport{agentID: agentID, repo: repo, ok: cloneErr == nil}
	if cloneErr != nil {
		report.reason = shortReason(cloneErr.Error())
	}
	st := &r.repos
	key := repoKey(agentID, repo)
	if last, ok := st.acked[key]; ok && last == report.outcome() {
		delete(st.pending, key)
		return
	}
	if st.pending == nil {
		st.pending = make(map[string]repoReport)
	}
	st.pending[key] = report
}

// flushRepoReports sends queued outcomes. An outcome stays queued until the
// server answers 2xx, or 404 (the server does not track that repository).
// A failed send is logged and never blocks cloning.
func (r *AgentReconciler) flushRepoReports(ctx context.Context) {
	if r.RepoReports == nil {
		return
	}
	st := &r.repos
	st.flushMu.Lock()
	defer st.flushMu.Unlock()
	st.mu.Lock()
	keys := make([]string, 0, len(st.pending))
	for key := range st.pending {
		keys = append(keys, key)
	}
	reports := make(map[string]repoReport, len(keys))
	for _, key := range keys {
		reports[key] = st.pending[key]
	}
	st.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		report := reports[key]
		err := r.RepoReports.ReportRepo(ctx, report.agentID, report.repo, report.ok, report.reason)
		if err != nil && !isNotFound(err) {
			log.Printf("machine reconcile: report repository %s for %s: %v", report.repo, report.agentID, err)
			continue
		}
		r.ackReport(key, report)
	}
}

func (r *AgentReconciler) ackReport(key string, report repoReport) {
	st := &r.repos
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pending[key] == report {
		delete(st.pending, key)
	}
	if st.acked == nil {
		st.acked = make(map[string]string)
	}
	st.acked[key] = report.outcome()
}

func isNotFound(err error) bool {
	var status interface{ HTTPStatus() int }
	return errors.As(err, &status) && status.HTTPStatus() == http.StatusNotFound
}
