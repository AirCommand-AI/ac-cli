package runmode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var safeRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var safeName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Clone struct{ Agent, Repo, Folder string }
type Rescuer struct {
	RunID      string
	Clones     []Clone
	LimitBytes int64
}

func (r Rescuer) Rescue(ctx context.Context) []RescueResult {
	results := make([]RescueResult, 0, len(r.Clones))
	for _, clone := range r.Clones {
		if ctx.Err() != nil {
			results = append(results, RescueResult{Agent: clone.Agent, Repo: clone.Repo, Result: "failed", Reason: "rescue deadline reached"})
			continue
		}
		results = append(results, r.rescue(ctx, clone))
	}
	return results
}
func (r Rescuer) rescue(ctx context.Context, c Clone) (result RescueResult) {
	result = RescueResult{Agent: c.Agent, Repo: c.Repo, Result: "failed"}
	if !safeName.MatchString(c.Agent) || !safeRepo.MatchString(c.Repo) || strings.Contains(c.Repo, "..") || !strings.HasPrefix(r.RunID, "run_") {
		result.Reason = "invalid rescue destination"
		return
	}
	branch := "aircommand/rescue/" + r.RunID + "/" + c.Agent
	result.Branch = branch
	folder := filepath.Join(c.Folder, strings.Split(c.Repo, "/")[1])
	info, err := os.Lstat(folder)
	if err != nil || !info.IsDir() {
		result.Reason = "repository folder missing or unsafe"
		return
	}
	gitDir, err := os.Lstat(filepath.Join(folder, ".git"))
	if err != nil || !gitDir.IsDir() {
		result.Reason = "repository metadata missing or unsafe"
		return
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", folder, "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, e := cmd.CombinedOutput()
		return string(out), e
	}
	remote, err := git("remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(remote) != "https://github.com/"+c.Repo+".git" {
		result.Reason = "origin is not the expected GitHub repository"
		return
	}
	lock := filepath.Join(folder, ".git", "index.lock")
	if info, e := os.Lstat(lock); e == nil {
		if !info.Mode().IsRegular() {
			result.Reason = "git index is locked"
			return
		}
		if e = os.Remove(lock); e != nil {
			result.Reason = "cannot remove stale index lock"
			return
		}
	}
	if out, e := git("add", "-A"); e != nil {
		result.Reason = "stage: " + short(out)
		return
	}
	names, e := git("diff", "--cached", "--name-only", "-z")
	if e != nil {
		result.Reason = "inspect staged work failed"
		return
	}
	capBytes := r.LimitBytes
	if capBytes <= 0 {
		capBytes = 50 << 20
	}
	var size int64
	for _, name := range strings.Split(names, "\x00") {
		if name == "" {
			continue
		}
		file := filepath.Clean(filepath.Join(folder, name))
		if !strings.HasPrefix(file, folder+string(os.PathSeparator)) {
			result.Reason = "staged path escapes repository"
			return
		}
		info, e := os.Lstat(file)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			result.Reason = "cannot stat staged file"
			return
		}
		size += info.Size()
		if size > capBytes {
			result.Reason = "staged work exceeds rescue size limit"
			return
		}
	}
	changed := names != ""
	if changed {
		cmd := exec.CommandContext(ctx, "git", "-C", folder, "-c", "core.hooksPath=/dev/null", "-c", "user.name="+c.Agent+" (AirCommand)", "-c", "user.email=run-agents@users.noreply.aircommand.ai", "commit", "--no-verify", "-m", "Rescue unpushed work for "+r.RunID)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, e := cmd.CombinedOutput()
		if e != nil {
			result.Reason = "commit: " + short(string(out))
			return
		}
	}
	upstream, e := git("rev-list", "--count", "@{u}..HEAD")
	if e == nil && strings.TrimSpace(upstream) == "0" && !changed {
		result.Result = "nothing"
		return
	}
	output, e := git("push", "origin", "HEAD:refs/heads/"+branch)
	if e != nil {
		result.Reason = "push: " + short(output)
		return
	}
	result.Result = "pushed"
	return
}
func short(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}
