package piadapter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncWritesNoOpAndPermissions(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	project := filepath.Join(home, "project/.pi/extensions/aircommand/index.ts")
	if err := os.MkdirAll(filepath.Dir(project), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("custom"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(home); err != nil {
		t.Fatal(err)
	}
	check := func() os.FileInfo {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, Source) {
			t.Fatalf("extension content: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("extension mode: %o", info.Mode().Perm())
		}
		return info
	}
	daemonPath := filepath.Join(filepath.Dir(path), "daemon.ts")
	if contents, err := os.ReadFile(daemonPath); err != nil || !bytes.Equal(contents, DaemonSource) {
		t.Fatalf("daemon add-on missing: %v", err)
	}
	first := check()
	if err := Sync(home); err != nil {
		t.Fatal(err)
	}
	second := check()
	if !os.SameFile(first, second) {
		t.Fatal("unchanged extension was replaced")
	}
	if err := os.WriteFile(path, []byte("old extension"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Sync(home); err != nil {
		t.Fatal(err)
	}
	check()
	data, err := os.ReadFile(project)
	if err != nil || string(data) != "custom" {
		t.Fatalf("project extension changed: %s, %v", data, err)
	}
	if err := VerifyHeadless(home); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyHeadlessRefusesStaleAndMissing(t *testing.T) {
	home := t.TempDir()
	if err := VerifyHeadless(home); err == nil || !strings.Contains(err.Error(), "daemon start") {
		t.Fatal(err)
	}
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old extension"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHeadless(home); err == nil || !strings.Contains(err.Error(), "--aircommand-headless") {
		t.Fatal(err)
	}
}
