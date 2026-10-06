package daemonclient

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Names may not start with "-" so a flag such as --help is never taken as a name.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)
var repoComponentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func ValidAgentName(name string) bool { return agentNamePattern.MatchString(name) }

// Workspace prepares only the requested agent's folder. Clone and fetch are
// injected so tests need no network, and the caller can use its own git setup.
type Workspace struct {
	Root string
	Git  func(args ...string) error
}

func (w Workspace) Prepare(name string, repos []string) (string, error) {
	if !ValidAgentName(name) {
		return "", fmt.Errorf("invalid agent name %q: use 1–64 letters, numbers, underscores or hyphens", name)
	}
	folder := filepath.Join(w.Root, name)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", fmt.Errorf("create agent work folder: %w", err)
	}
	git := w.Git
	if git == nil {
		git = func(args ...string) error { return exec.Command("git", args...).Run() }
	}
	for _, repo := range repos {
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || !repoComponentPattern.MatchString(parts[0]) || !repoComponentPattern.MatchString(parts[1]) || parts[0] == "." || parts[1] == "." || parts[0] == ".." || parts[1] == ".." || strings.HasPrefix(parts[0], "-") || strings.HasPrefix(parts[1], "-") {
			return "", fmt.Errorf("invalid repository %q: expected owner/repo", repo)
		}
		path := filepath.Join(folder, parts[1])
		if info, err := os.Stat(path); err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("repository destination %s is not a directory", path)
			}
			if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
				return "", fmt.Errorf("repository destination %s is not a git checkout", path)
			}
			if err := git("-C", path, "fetch", "origin"); err != nil {
				return "", fmt.Errorf("fetch %s: %w", repo, err)
			}
		} else if os.IsNotExist(err) {
			if err := git("clone", "--", "https://github.com/"+repo+".git", path); err != nil {
				return "", fmt.Errorf("clone %s: %w", repo, err)
			}
		} else {
			return "", err
		}
	}
	return folder, nil
}

// WriteBrief creates the phase-1 identity and unread-message start-up guidance.
func WriteBrief(path, name, organization, workstream string) error {
	if !ValidAgentName(name) {
		return fmt.Errorf("invalid agent name %q", name)
	}
	if organization == "" || workstream == "" || strings.ContainsAny(organization+workstream, "\r\n") {
		return fmt.Errorf("invalid workspace or workstream")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	brief := fmt.Sprintf("You are %s in workstream %s (workspace %s). Work only in ~/work/%s. When you start, read your unread AirCommand messages (aircom inbox) and handle them.\n", name, workstream, organization, name)
	return os.WriteFile(path, []byte(brief), 0o600)
}
