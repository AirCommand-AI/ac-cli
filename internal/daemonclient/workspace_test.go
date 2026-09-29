package daemonclient

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceCloneFetchAndBrief(t *testing.T) {
	root := t.TempDir()
	var commands [][]string
	w := Workspace{Root: root, Git: func(args ...string) error { commands = append(commands, append([]string(nil), args...)); return nil }}
	folder, err := w.Prepare("eng-3", []string{"AirCommand-AI/ac-cli"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clone", "--", "https://github.com/AirCommand-AI/ac-cli.git", filepath.Join(folder, "ac-cli")}
	if !reflect.DeepEqual(commands[0], want) {
		t.Fatalf("clone: %v", commands[0])
	}
	if err := os.MkdirAll(filepath.Join(folder, "ac-cli", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prepare("eng-3", []string{"AirCommand-AI/ac-cli"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands[1], []string{"-C", filepath.Join(folder, "ac-cli"), "fetch", "origin"}) {
		t.Fatalf("fetch: %v", commands[1])
	}
	briefPath := filepath.Join(root, "state", "brief.md")
	if err := WriteBrief(briefPath, "eng-3", "Air Command", "626"); err != nil {
		t.Fatal(err)
	}
	brief, err := os.ReadFile(briefPath)
	if err != nil || !strings.Contains(string(brief), "read your unread AirCommand messages (aircom inbox)") {
		t.Fatalf("brief: %q %v", brief, err)
	}
}

func TestWorkspaceRejectsUnsafeNamesAndRepos(t *testing.T) {
	w := Workspace{Root: t.TempDir(), Git: func(...string) error { t.Fatal("git must not run"); return nil }}
	for _, name := range []string{"../escape", "bad.name", "", "name:window", strings.Repeat("x", 65)} {
		if _, err := w.Prepare(name, nil); err == nil {
			t.Errorf("accepted name %q", name)
		}
	}
	for _, repo := range []string{"../evil", "owner/..", "owner/repo/extra", "-flag/repo", "owner/-flag", "owner\\repo"} {
		if _, err := w.Prepare("safe", []string{repo}); err == nil {
			t.Errorf("accepted repo %q", repo)
		}
	}
}
