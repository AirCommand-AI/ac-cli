package runmode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const (
	ghCommand     = "gh"
	ghTokenVar    = "GH_TOKEN"
	githubToken   = "GITHUB_TOKEN"
	ghRepoVar     = "GH_REPO"
	pathVar       = "PATH"
	githubHost    = "github.com"
	githubURLHTTP = "https://github.com/"
)

// GHShimDirectory holds the gh wrapper that run agents find first on PATH.
func GHShimDirectory(home string) string { return filepath.Join(storagepath.Root(home), "run-bin") }

// InstallGHShim writes a gh wrapper that hands each gh call the token of the
// repository's owner, and puts it first on this process's PATH so agents
// started by the daemon inherit it. hosts.yml keeps only the default token.
func InstallGHShim(home, aircomPath string) error {
	if !filepath.IsAbs(aircomPath) {
		return errors.New("aircom path must be absolute")
	}
	dir := GHShimDirectory(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' %s \"$@\"\n", strings.ReplaceAll(aircomPath, "'", `'\''`), ghCommand)
	tmp := filepath.Join(dir, ".gh.tmp")
	if err := os.WriteFile(tmp, []byte(script), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, ghCommand)); err != nil {
		return err
	}
	current := os.Getenv(pathVar)
	for _, entry := range filepath.SplitList(current) {
		if entry == dir {
			return nil
		}
	}
	return os.Setenv(pathVar, dir+string(os.PathListSeparator)+current)
}

// RunGH replaces this process with the real gh, carrying the token for the
// repository the command targets. Without a known owner gh uses hosts.yml.
func RunGH(ctx context.Context, home string, args []string) error {
	real, err := realGH(GHShimDirectory(home))
	if err != nil {
		return err
	}
	env := os.Environ()
	if os.Getenv(ghTokenVar) == "" && os.Getenv(githubToken) == "" {
		owner := GHOwner(args, os.Getenv(ghRepoVar), func() string { return remoteOwner(ctx) })
		if owner != "" {
			if token, e := RequestToken(ctx, home, owner); e == nil {
				env = append(env, ghTokenVar+"="+token)
			}
		}
	}
	return syscall.Exec(real, append([]string{ghCommand}, args...), env)
}

func realGH(shim string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv(pathVar)) {
		if dir == "" || filepath.Clean(dir) == filepath.Clean(shim) {
			continue
		}
		candidate := filepath.Join(dir, ghCommand)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("gh is not installed")
}

// GHOwner finds the repository owner a gh command targets: --repo/-R, a
// github.com URL argument, an api repos/ path, a repo subcommand argument,
// GH_REPO, then the current repository's origin remote.
func GHOwner(args []string, ghRepo string, remote func() string) string {
	for i, arg := range args {
		value := ""
		switch {
		case arg == "-R" || arg == "--repo":
			if i+1 < len(args) {
				value = args[i+1]
			}
		case strings.HasPrefix(arg, "--repo="):
			value = strings.TrimPrefix(arg, "--repo=")
		case strings.HasPrefix(arg, "-R") && len(arg) > 2:
			value = arg[2:]
		}
		if owner := OwnerFromRepoRef(value); owner != "" {
			return owner
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, githubURLHTTP) {
			if owner := OwnerFromRepoRef(arg); owner != "" {
				return owner
			}
		}
	}
	if len(args) > 0 && args[0] == "api" {
		for _, arg := range args[1:] {
			if rest, ok := strings.CutPrefix(strings.TrimPrefix(arg, "/"), "repos/"); ok {
				if owner := OwnerFromRepoRef(rest); owner != "" {
					return owner
				}
			}
		}
	}
	if len(args) > 2 && args[0] == "repo" && !strings.HasPrefix(args[2], "-") {
		if owner := OwnerFromRepoRef(args[2]); owner != "" {
			return owner
		}
	}
	if owner := OwnerFromRepoRef(ghRepo); owner != "" {
		return owner
	}
	if remote != nil {
		return remote()
	}
	return ""
}

// OwnerFromRepoRef reads the owner from owner/repo, [HOST/]OWNER/REPO, an
// https URL or an scp-style git remote. Hosts other than github.com yield "".
func OwnerFromRepoRef(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, prefix := range []string{"https://", "http://", "ssh://", "git@"} {
		ref = strings.TrimPrefix(ref, prefix)
	}
	if rest, ok := strings.CutPrefix(ref, githubHost); ok {
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, ":") {
			return ""
		}
		ref = rest[1:]
	} else if host, _, found := strings.Cut(ref, "/"); found && strings.Contains(host, ".") {
		return ""
	}
	owner, repo, found := strings.Cut(ref, "/")
	if !found || owner == "" || repo == "" || strings.ContainsAny(owner, ": \t") {
		return ""
	}
	return owner
}

func remoteOwner(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return OwnerFromRepoRef(strings.TrimSpace(string(out)))
}
