package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
)

// ProcessSnapshot is injectable so ancestry and pi discovery are tested without
// depending on the test runner's shell or a particular /proc implementation.
type ProcessSnapshot func(pid int) (parent int, command, start string, err error)

func systemProcess(pid int) (int, string, string, error) {
	read := func(field string) (string, error) {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", field+"=").Output()
		return strings.TrimSpace(string(out)), err
	}
	parentText, err := read("ppid")
	if err != nil {
		return 0, "", "", err
	}
	parent, err := strconv.Atoi(parentText)
	if err != nil {
		return 0, "", "", err
	}
	command, err := read("command")
	if err != nil {
		return 0, "", "", err
	}
	var start string
	if runtime.GOOS == "linux" {
		data, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if e != nil {
			return 0, "", "", e
		}
		fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
		if len(fields) < 20 {
			return 0, "", "", fmt.Errorf("invalid process stat for %d", pid)
		}
		start = fields[19]
	} else {
		start, err = read("lstart")
		if err != nil {
			return 0, "", "", err
		}
	}
	return parent, command, start, nil
}

// discoverSession ignores intermediate shells and the aircom process itself.
// A pid cannot be supplied by a CLI flag: only this ancestry can be attached.
func discoverSession(pid int, snapshot ProcessSnapshot) (daemonclient.SessionAttach, error) {
	if snapshot == nil {
		snapshot = systemProcess
	}
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		parent, command, start, err := snapshot(pid)
		if err != nil {
			return daemonclient.SessionAttach{}, fmt.Errorf("inspect parent process %d: %w", pid, err)
		}
		parts := strings.Fields(command)
		if len(parts) > 0 {
			executable := strings.ToLower(filepath.Base(parts[0]))
			lower := strings.ToLower(command)
			if (executable == "node" || executable == "bun" || executable == "pi") && strings.Contains(lower, "pi-coding-agent/") {
				return daemonclient.SessionAttach{SessionPID: pid, SessionStart: start, Program: "pi"}, nil
			}
			if executable == "claude" || executable == "codex" {
				return daemonclient.SessionAttach{SessionPID: pid, SessionStart: start, Program: "other"}, nil
			}
		}
		if parent == pid {
			break
		}
		pid = parent
	}
	return daemonclient.SessionAttach{}, &publicError{message: "No supported agent program found in this command's parent processes. Run aircom join from a pi or Claude Code session, not a standalone shell."}
}
