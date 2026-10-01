//go:build linux

package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBootStopsRecordedHeadlessPiBeforeRestart(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+1:])
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode, d.Desired, d.State = "headless", "running", "running"
	d.Pi = &PiProcess{PID: pid, PGID: pid, StartTime: fields[19], Cmdline: "renamed pi"}
	path := m.definitionPath(d.AgentID)
	if err := atomicJSON(path, d); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(m.briefPath(d.AgentID)), 0700); err != nil {
		t.Fatal(err)
	}
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	m.mu.Lock()
	err = m.bootAgent(context.Background(), path)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		// A zombie is still visible until the test reaps the child.
		current, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil && !strings.HasPrefix(string(current)[strings.LastIndex(string(current), ")")+1:], " Z ") {
			t.Fatal("old headless pi survived boot")
		}
	}
	if m.agents[d.Name].driver == nil {
		t.Fatal("replacement driver not launched")
	}
}

func TestKillRecordedPiFencesStartTimeAndWaits(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+1:])
	p := &PiProcess{PID: pid, PGID: pid, StartTime: fields[19], Cmdline: "pi changed its title"}
	wrong := *p
	wrong.StartTime = "0"
	if err := killRecordedPi(context.Background(), &wrong); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("mismatched identity killed a different process: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := killRecordedPi(ctx, p); err != nil {
		t.Fatal(err)
	}
}
