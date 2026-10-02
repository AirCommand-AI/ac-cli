//go:build linux || darwin

package supervisor

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestManualMachineStopSignalsOnlyFencedTakeoverGroup(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	m.NewDriver = func(io.Writer) pidriver.Driver { return pidriver.NewFake() }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Takeover(ctx, d.Name); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	if err := m.RecordTakeover(d.Name, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	notices := []string{}
	m.SetTakeoverNotice(d.Name, func(s string) error { notices = append(notices, s); return nil })
	if err := m.SetMachineState(ctx, "stopping"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("foreground pi did not exit")
	}
	if len(notices) != 1 || notices[0] != "machine is stopping; your pi was closed" || !m.AgentsStopped() || m.agents[d.Name].def.Desired != "running" {
		t.Fatalf("notices %v stopped %v desired %s", notices, m.AgentsStopped(), m.agents[d.Name].def.Desired)
	}
}

func TestTakeoverSignalEscalatesWhenForegroundIgnoresTERM(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; exec sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	p := &TakeoverProcess{PID: cmd.Process.Pid, StartTime: takeoverStartTime(cmd.Process.Pid)}
	// Wait for the shell to install its ignored TERM disposition before sending.
	time.Sleep(100 * time.Millisecond)
	begin := time.Now()
	if err := terminateTakeoverGroup(context.Background(), p, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) < 150*time.Millisecond {
		t.Fatal("did not allow TERM grace before escalation")
	}
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("SIGKILL did not end the process group")
	}
}
func TestTakeoverSignalRejectsReusedPIDAndSharedProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	p := &TakeoverProcess{PID: cmd.Process.Pid, StartTime: "not-this-process"}
	if err := terminateTakeoverGroup(context.Background(), p, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !takeoverAlive(&TakeoverProcess{PID: p.PID, StartTime: takeoverStartTime(p.PID)}) {
		t.Fatal("wrong fence killed process")
	}
	shared := &TakeoverProcess{PID: syscall.Getpid(), StartTime: takeoverStartTime(syscall.Getpid())}
	if err := terminateTakeoverGroup(context.Background(), shared, time.Millisecond); err == nil {
		t.Fatal("would signal CLI process group")
	}
}
