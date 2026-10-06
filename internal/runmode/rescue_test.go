package runmode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRescueStagesWithinLimitBypassesHooksAndReportsPushRejection(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(bin, 0o700)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GIT_LOG"
case " $* " in
  *" remote get-url origin "*) printf '%s\n' https://github.com/Org/repo.git ;;
  *" diff --cached --name-only -z "*) printf 'new.txt\000' ;;
  *" push origin "*) echo 'push protection rejected' >&2;exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "git.log")
	t.Setenv("GIT_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	folder := filepath.Join(root, "work", "eng-1")
	repo := filepath.Join(folder, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o700)
	_ = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new work"), 0o600)
	lock := filepath.Join(repo, ".git", "index.lock")
	_ = os.WriteFile(lock, []byte("old"), 0o600)
	result := (Rescuer{RunID: "run_0123456789abcdef01234567", Clones: []Clone{{Agent: "eng-1", Repo: "Org/repo", Folder: folder}}}).Rescue(context.Background())
	if len(result) != 1 || result[0].Result != "failed" || !strings.Contains(result[0].Reason, "push protection") || !strings.HasPrefix(result[0].Branch, "aircommand/rescue/run_") {
		t.Fatalf("rescue=%+v", result)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale index lock remains: %v", err)
	}
	calls, _ := os.ReadFile(logPath)
	for _, part := range []string{"core.hooksPath=/dev/null", "commit --no-verify", "user.name=eng-1 (AirCommand)", "push origin HEAD:refs/heads/aircommand/rescue/"} {
		if !strings.Contains(string(calls), part) {
			t.Errorf("missing %q in git calls: %s", part, calls)
		}
	}
}
func TestRescuePushesUnpushedCommitsAndSkipsAlreadyPushedRepo(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "eng-1")
	repo := filepath.Join(folder, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o700)
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(bin, 0o700)
	script := `#!/bin/sh
case " $* " in
 *" remote get-url origin "*) echo https://github.com/Org/repo.git ;;
 *" rev-list --count "*) echo "$UPSTREAM_COUNT" ;;
 *" push origin "*) printf '%s\n' "$*" > "$PUSH_LOG" ;;
esac
`
	_ = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PUSH_LOG", filepath.Join(root, "push.log"))
	rescuer := Rescuer{RunID: "run_example", Clones: []Clone{{Agent: "eng-1", Repo: "Org/repo", Folder: folder}}}
	t.Setenv("UPSTREAM_COUNT", "0")
	result := rescuer.Rescue(context.Background())
	if len(result) != 1 || result[0].Result != "nothing" {
		t.Fatal(result)
	}
	t.Setenv("UPSTREAM_COUNT", "2")
	result = rescuer.Rescue(context.Background())
	if len(result) != 1 || result[0].Result != "pushed" {
		t.Fatal(result)
	}
	pushed, err := os.ReadFile(filepath.Join(root, "push.log"))
	if err != nil || !strings.Contains(string(pushed), "HEAD:refs/heads/aircommand/rescue/run_example/eng-1") {
		t.Fatalf("push %q %v", pushed, err)
	}
}

func TestRescueRejectsOversizedFileBeforeCommit(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(repo, "big.bin"), []byte("123456789"), 0o600)
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(bin, 0o700)
	log := filepath.Join(root, "git.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GIT_LOG"
case " $* " in
  *" remote get-url origin "*) echo https://github.com/Org/repo.git ;;
  *" diff --cached --name-only -z "*) printf 'big.bin\000' ;;
esac
`
	_ = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700)
	t.Setenv("GIT_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := (Rescuer{RunID: "run_example", Clones: []Clone{{Agent: "eng-1", Repo: "Org/repo", Folder: root}}, LimitBytes: 4}).Rescue(context.Background())
	if len(result) != 1 || result[0].Reason != "staged work exceeds rescue size limit" {
		t.Fatal(result)
	}
	commands, _ := os.ReadFile(log)
	if strings.Contains(string(commands), " commit ") || strings.Contains(string(commands), " push ") {
		t.Fatalf("unsafe git calls: %s", commands)
	}
}
