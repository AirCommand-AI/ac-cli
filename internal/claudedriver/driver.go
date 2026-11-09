// Package claudedriver drives one headless Claude Code process per agent
// (`claude -p` with stream-json input and output) behind the pidriver.Driver
// interface. Events are translated to the pi event kinds the supervisor and
// the attach viewer already understand. Methods must not be called while
// holding the supervisor mutex.
package claudedriver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

// ErrAPIKey: Claude Code reported a credential other than the subscription.
// The agent must not be restarted.
var ErrAPIKey = errors.New("Claude Code is using an API key, not the Claude subscription")

// interruptTimeout bounds the wait for Claude Code to confirm an interrupt.
const interruptTimeout = 10 * time.Second

// Driver owns the claude process. One writer owns stdin; the reader queues
// stdout lines and the run loop handles them, so consumers never block it.
type Driver struct {
	opts      pidriver.Options
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	configDir string
	sessionID string
	ready     chan struct{}
	exited    chan pidriver.Exit
	done      chan struct{}
	readDone  chan struct{}
	events    chan pidriver.Event
	notify    chan struct{}
	writes    chan []byte
	lines     [][]byte
	outgoing  []pidriver.Outgoing
	state     pidriver.Snapshot
	tools     map[string]string // tool_use id → name
	closed    bool
	stopping  bool
	seq       int
	// interrupting holds the message to send once the interrupt is confirmed.
	interrupting   string
	interruptMsg   pidriver.Outgoing
	interruptUntil time.Time
	refusal        error
}

// New returns a driver; opts.Log receives stderr.
func New(opts pidriver.Options) pidriver.Driver {
	return &Driver{opts: opts, ready: make(chan struct{}), exited: make(chan pidriver.Exit, 1), done: make(chan struct{}), readDone: make(chan struct{}), events: make(chan pidriver.Event, 256), notify: make(chan struct{}, 1), writes: make(chan []byte, 256), tools: map[string]string{}}
}

func (d *Driver) Ready() <-chan struct{}        { return d.ready }
func (d *Driver) Exited() <-chan pidriver.Exit  { return d.exited }
func (d *Driver) Events() <-chan pidriver.Event { return d.events }
func (d *Driver) State() pidriver.Snapshot      { d.mu.Lock(); defer d.mu.Unlock(); return d.state }
func (d *Driver) now() time.Time {
	if d.opts.Clock != nil {
		return d.opts.Clock()
	}
	return time.Now()
}
func (d *Driver) wake() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

// SessionUUID turns an agent ID (agm_ + 32 hex) into the UUID Claude Code
// requires for --session-id.
func SessionUUID(agentID string) (string, error) {
	hex := strings.TrimPrefix(agentID, "agm_")
	if len(hex) != 32 || strings.Trim(strings.ToLower(hex), "0123456789abcdef") != "" {
		return "", fmt.Errorf("agent ID %q has no session UUID", agentID)
	}
	hex = strings.ToLower(hex)
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:], nil
}

// transcript returns the session's transcript path, "" when none exists yet.
func transcript(configDir, sessionID string) string {
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// Start launches claude. spec.PiPath is the claude binary, spec.SessionID the
// session UUID, spec.Env the environment (it must set CLAUDE_CONFIG_DIR).
func (d *Driver) Start(spec pidriver.LaunchSpec) error {
	d.mu.Lock()
	if d.cmd != nil || d.closed {
		d.mu.Unlock()
		return errors.New("driver already started or stopped")
	}
	d.mu.Unlock()
	configDir := ""
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			configDir = v
		}
	}
	if configDir == "" || spec.SessionID == "" {
		return errors.New("claude launch needs CLAUDE_CONFIG_DIR and a session ID")
	}
	path := spec.PiPath
	if path == "" {
		path = "claude"
	}
	session := "--session-id"
	if transcript(configDir, spec.SessionID) != "" {
		session = "--resume"
	}
	args := append([]string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", session, spec.SessionID}, spec.Args...)
	cmd := exec.Command(path, args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = d.opts.Log
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err = cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return err
	}
	_ = stdoutWriter.Close()
	d.mu.Lock()
	d.cmd, d.stdin, d.configDir, d.sessionID = cmd, stdin, configDir, spec.SessionID
	d.state.PID, d.state.PGID, d.state.StartTime = cmd.Process.Pid, cmd.Process.Pid, processStartTime(cmd.Process.Pid)
	d.state.Cmdline = append([]string{path}, args...)
	d.state.Ready = true
	d.mu.Unlock()
	go func() { defer stdout.Close(); d.read(stdout) }()
	go d.write()
	runDone := make(chan struct{})
	go func() { defer close(runDone); d.run() }()
	go d.wait(cmd, stdout, runDone)
	// Claude Code reads stdin at once; the first message starts the first turn.
	close(d.ready)
	return nil
}

func (d *Driver) wait(cmd *exec.Cmd, stdout *os.File, runDone <-chan struct{}) {
	err := cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-d.readDone:
	case <-time.After(2 * time.Second):
		_ = stdout.Close()
		<-d.readDone
	}
	exit := pidriver.Exit{Err: err}
	if ps := cmd.ProcessState; ps != nil {
		exit.Code = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			exit.Signal = ws.Signal().String()
		}
	}
	d.mu.Lock()
	d.closed = true
	if d.refusal != nil {
		exit.Err = d.refusal
	}
	close(d.writes)
	d.mu.Unlock()
	d.wake()
	<-runDone
	d.reportUndelivered()
	d.exited <- exit
	close(d.done)
}

func (d *Driver) read(r io.Reader) {
	defer close(d.readDone)
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			d.mu.Lock()
			d.lines = append(d.lines, trimmed)
			d.mu.Unlock()
			d.wake()
		}
		if err != nil {
			return
		}
	}
}

func (d *Driver) write() {
	for data := range d.writes {
		d.mu.Lock()
		stdin := d.stdin
		d.mu.Unlock()
		if _, err := stdin.Write(data); err != nil {
			return
		}
	}
}

func (d *Driver) enqueue(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	// Sent under the lock so wait cannot close writes in between.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	select {
	case d.writes <- append(data, '\n'):
	default:
		// stdin is backed up (256 lines); the process is not reading.
	}
}

// Send queues a message. Regular and urgent are the same for Claude Code: a
// message written during a turn joins it. Interrupt stops the turn first.
func (d *Driver) Send(msg pidriver.Outgoing) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.stopping {
		return errors.New("claude exited or stopping")
	}
	d.outgoing = append(d.outgoing, msg)
	d.wake()
	return nil
}

func (d *Driver) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case _, ok := <-d.notify:
			if !ok {
				return
			}
		case <-ticker.C:
		}
		for {
			d.mu.Lock()
			if len(d.lines) == 0 {
				d.mu.Unlock()
				break
			}
			line := d.lines[0]
			d.lines = d.lines[1:]
			d.mu.Unlock()
			d.handle(line)
		}
		d.flushOutgoing()
		d.mu.Lock()
		done := d.closed && len(d.lines) == 0
		d.mu.Unlock()
		if done {
			return
		}
	}
}

func (d *Driver) flushOutgoing() {
	for {
		d.mu.Lock()
		if d.interrupting != "" && d.now().After(d.interruptUntil) {
			// No confirmation: send the message anyway rather than lose it.
			d.interrupting = ""
			d.outgoing = append([]pidriver.Outgoing{d.interruptMsg}, d.outgoing...)
		}
		if d.closed || d.interrupting != "" || len(d.outgoing) == 0 {
			d.mu.Unlock()
			return
		}
		msg := d.outgoing[0]
		d.outgoing = d.outgoing[1:]
		inTurn := d.state.Streaming
		if msg.Kind == pidriver.Interrupt && inTurn {
			d.seq++
			id := fmt.Sprintf("aircom-interrupt-%d", d.seq)
			d.interrupting, d.interruptMsg, d.interruptUntil = id, pidriver.Outgoing{Text: msg.Text, Kind: pidriver.Urgent, Source: msg.Source}, d.now().Add(interruptTimeout)
			d.mu.Unlock()
			d.enqueue(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}})
			continue
		}
		d.mu.Unlock()
		d.sendUser(msg.Text)
	}
}

func (d *Driver) sendUser(text string) {
	if !strings.HasPrefix(text, "[AirCommand] ") {
		text = "[AirCommand] " + text
	}
	d.enqueue(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}, "parent_tool_use_id": nil})
	d.emit(pidriver.Event{Kind: "message_start", Data: map[string]any{"message": map[string]any{"role": "user", "content": text}}, At: d.now()})
}

func (d *Driver) emit(ev pidriver.Event) {
	select {
	case d.events <- ev:
	default:
		select {
		case <-d.events:
		default:
		}
		select {
		case d.events <- ev:
		default:
		}
	}
}

func (d *Driver) reportUndelivered() {
	d.mu.Lock()
	remaining := append([]pidriver.Outgoing(nil), d.outgoing...)
	if d.interrupting != "" {
		remaining = append(remaining, d.interruptMsg)
	}
	d.mu.Unlock()
	for _, msg := range remaining {
		d.emit(pidriver.Event{Kind: "undelivered", Data: map[string]any{"text": msg.Text, "kind": msg.Kind, "source": msg.Source}, At: d.now()})
	}
}

// Stop closes stdin, then terminates and kills the process group.
func (d *Driver) Stop(ctx context.Context) error {
	d.mu.Lock()
	cmd, stdin := d.cmd, d.stdin
	d.stopping = true
	d.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = stdin.Close()
	wait := func(grace time.Duration, interruptible bool) bool {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		if interruptible {
			select {
			case <-d.done:
				return true
			case <-ctx.Done():
				return false
			case <-timer.C:
				return false
			}
		}
		select {
		case <-d.done:
			return true
		case <-timer.C:
			return false
		}
	}
	if wait(5*time.Second, true) {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	if wait(3*time.Second, false) {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if wait(2*time.Second, false) {
		return nil
	}
	return errors.New("claude group did not exit after SIGKILL")
}

func processStartTime(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

var _ pidriver.Driver = (*Driver)(nil)
