package pidriver

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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// RPC owns the subprocess and the RPC pipe. One dedicated writer owns stdin;
// the stdout reader appends to an unbounded queue and never waits for
// event consumers or command processing.
type RPC struct {
	opts           Options
	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	ready          chan struct{}
	exited         chan Exit
	done           chan struct{}
	events         chan Event
	notify         chan struct{}
	writerNotify   chan struct{}
	writes         [][]byte
	starting       bool
	lines          [][]byte
	outgoing       []Outgoing
	state          Snapshot
	closed         bool
	compacting     bool
	seq            int
	pending        map[int]Outgoing
	tools          map[string]string
	interruptID    int
	interruptPhase string
	interruptMsg   Outgoing
	restore        []string
}

func New(opts Options) Driver {
	return &RPC{opts: opts, ready: make(chan struct{}), exited: make(chan Exit, 1), done: make(chan struct{}), events: make(chan Event, 256), notify: make(chan struct{}, 1), writerNotify: make(chan struct{}, 1), pending: make(map[int]Outgoing), tools: make(map[string]string)}
}
func (d *RPC) Ready() <-chan struct{} { return d.ready }
func (d *RPC) Exited() <-chan Exit    { return d.exited }
func (d *RPC) Events() <-chan Event   { return d.events }
func (d *RPC) State() Snapshot        { d.mu.Lock(); defer d.mu.Unlock(); return d.state }
func (d *RPC) wake() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func checkVersion(path string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pi --version: %w: %s", err, out)
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return fmt.Errorf("unrecognized pi version: %q", out)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	if a == 0 && (b < 87 || b == 87 && c < 1) {
		return fmt.Errorf("headless mode requires pi >= 0.87.1, found %s", m[0])
	}
	return nil
}
func (d *RPC) Start(spec LaunchSpec) error {
	d.mu.Lock()
	if d.cmd != nil || d.closed || d.starting {
		d.mu.Unlock()
		return errors.New("driver already started or stopped")
	}
	d.starting = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.starting = false; d.mu.Unlock() }()
	if spec.PiPath == "" {
		spec.PiPath = "pi"
	}
	if err := checkVersion(spec.PiPath); err != nil {
		return err
	}
	args := []string{"--mode", "rpc", "--session-id", spec.SessionID}
	if spec.ForkFrom != "" {
		args = append(args, "--fork", spec.ForkFrom)
	}
	args = append(args, spec.Args...)
	cmd := exec.Command(spec.PiPath, args...)
	cmd.Dir = spec.WorkDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = d.opts.Log
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	d.mu.Lock()
	d.cmd = cmd
	d.stdin = stdin
	d.state.PID = cmd.Process.Pid
	d.state.PGID = cmd.Process.Pid
	d.state.StartTime = processStartTime(cmd.Process.Pid)
	// Cmdline is informational only: node changes process.title after launch.
	// Populate it from /proc when pi confirms readiness, never use it as a kill fence.
	d.mu.Unlock()
	go d.read(stdout)
	go d.write()
	go d.run()
	go func() {
		err := cmd.Wait()
		exit := Exit{Err: err}
		if ps := cmd.ProcessState; ps != nil {
			exit.Code = ps.ExitCode()
			if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				exit.Signal = ws.Signal().String()
			}
		}
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		d.exited <- exit
		close(d.done)
		d.wake()
	}()
	d.command("get_state", nil)
	return nil
}
func processCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, string(part))
	}
	return result
}
func processStartTime(pid int) string {
	// Linux /proc stat field 22. comm may contain spaces and parentheses.
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
func (d *RPC) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			d.mu.Lock()
			d.lines = append(d.lines, bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'}))
			d.mu.Unlock()
			d.wake()
		}
		if err != nil {
			return
		}
	}
}
func (d *RPC) command(kind string, fields map[string]any) int {
	d.mu.Lock()
	d.seq++
	id := d.seq
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["type"] = kind
	if _, ok := fields["id"]; !ok {
		fields["id"] = strconv.Itoa(id)
	}
	data, _ := json.Marshal(fields)
	if !d.closed {
		d.writes = append(d.writes, append(data, '\n'))
		select {
		case d.writerNotify <- struct{}{}:
		default:
		}
	}
	d.mu.Unlock()
	return id
}
func (d *RPC) write() {
	for range d.writerNotify {
		for {
			d.mu.Lock()
			if len(d.writes) == 0 || d.closed {
				done := d.closed
				d.mu.Unlock()
				if done {
					return
				}
				break
			}
			data := d.writes[0]
			d.writes[0] = nil
			d.writes = d.writes[1:]
			stdin := d.stdin
			d.mu.Unlock()
			if _, err := stdin.Write(data); err != nil {
				return
			}
		}
	}
}
func (d *RPC) Send(msg Outgoing) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("pi exited")
	}
	d.outgoing = append(d.outgoing, msg)
	d.wake()
	return nil
}
func (d *RPC) run() {
	for range d.notify {
		for {
			d.mu.Lock()
			if len(d.lines) == 0 {
				d.mu.Unlock()
				break
			}
			line := d.lines[0]
			d.lines[0] = nil
			d.lines = d.lines[1:]
			d.mu.Unlock()
			d.handle(line)
		}
		for {
			d.mu.Lock()
			if !d.state.Ready || d.compacting || d.interruptPhase != "" || len(d.outgoing) == 0 || d.closed {
				done := d.closed
				d.mu.Unlock()
				if done {
					return
				}
				break
			}
			msg := d.outgoing[0]
			d.outgoing = d.outgoing[1:]
			d.mu.Unlock()
			d.dispatch(msg)
		}
	}
}
func (d *RPC) dispatch(msg Outgoing) {
	if msg.Kind == Interrupt {
		id := d.command("clear_queue", nil)
		d.mu.Lock()
		d.interruptID = id
		d.interruptPhase = "clear_queue"
		d.interruptMsg = msg
		d.mu.Unlock()
		return
	}
	behavior := "followUp"
	if msg.Kind == Urgent || msg.Kind == Interrupt {
		behavior = "steer"
	}
	text := msg.Text
	if !strings.HasPrefix(text, "[AirCommand] ") {
		text = "[AirCommand] " + text
	}
	id := d.command("prompt", map[string]any{"message": text, "streamingBehavior": behavior})
	d.mu.Lock()
	d.pending[id] = msg
	d.mu.Unlock()
}
func (d *RPC) advanceInterrupt(v map[string]any) {
	d.mu.Lock()
	phase := d.interruptPhase
	msg := d.interruptMsg
	d.mu.Unlock()
	switch phase {
	case "clear_queue":
		if data, ok := v["data"].(map[string]any); ok {
			for _, key := range []string{"steering", "followUp"} {
				if items, ok := data[key].([]any); ok {
					for _, item := range items {
						if text, ok := item.(string); ok {
							d.restore = append(d.restore, text)
						}
					}
				}
			}
		}
		id := d.command("abort", nil)
		d.mu.Lock()
		d.interruptID = id
		d.interruptPhase = "abort"
		d.mu.Unlock()
	case "abort":
		text := msg.Text
		if !strings.HasPrefix(text, "[AirCommand] ") {
			text = "[AirCommand] " + text
		}
		id := d.command("prompt", map[string]any{"message": text, "streamingBehavior": "steer"})
		d.mu.Lock()
		d.interruptID = id
		d.interruptPhase = "prompt"
		d.mu.Unlock()
	case "prompt":
		d.mu.Lock()
		for _, text := range d.restore {
			d.outgoing = append(d.outgoing, Outgoing{Text: text, Kind: Regular})
		}
		d.restore = nil
		d.interruptPhase = ""
		d.mu.Unlock()
		d.wake()
	}
}
func (d *RPC) handle(line []byte) {
	var v map[string]any
	if json.Unmarshal(line, &v) != nil {
		return
	}
	typ, _ := v["type"].(string)
	if idstr, ok := v["id"].(string); ok {
		id, _ := strconv.Atoi(idstr)
		d.mu.Lock()
		interruptResponse := d.interruptPhase != "" && id == d.interruptID
		d.mu.Unlock()
		if interruptResponse {
			d.advanceInterrupt(v)
		}
		d.mu.Lock()
		msg, found := d.pending[id]
		delete(d.pending, id)
		d.mu.Unlock()
		if found && v["success"] == false {
			reason, _ := v["error"].(string)
			if strings.Contains(strings.ToLower(reason), "compact") {
				d.mu.Lock()
				d.outgoing = append([]Outgoing{msg}, d.outgoing...)
				d.compacting = true
				d.mu.Unlock()
			}
		}
		if typ == "response" && v["command"] == "get_state" && v["success"] == true {
			d.mu.Lock()
			if !d.state.Ready {
				d.state.Cmdline = processCmdline(d.state.PID)
				d.state.Ready = true
				close(d.ready)
			}
			d.mu.Unlock()
			d.wake()
		}
	}
	if typ == "compaction_start" {
		d.mu.Lock()
		d.compacting = true
		d.mu.Unlock()
	}
	if typ == "compaction_end" {
		d.mu.Lock()
		d.compacting = false
		d.mu.Unlock()
		d.wake()
	}
	if typ == "extension_ui_request" {
		method, _ := v["method"].(string)
		switch method {
		case "select", "confirm", "input", "editor":
			d.command("extension_ui_response", map[string]any{"id": v["id"], "cancelled": true})
			typ = "dialog_cancelled"
		}
	}
	d.mu.Lock()
	now := time.Now
	if d.opts.Clock != nil {
		now = d.opts.Clock
	}
	d.state.LastEvent = now()
	switch typ {
	case "agent_start":
		d.state.Streaming = true
		d.state.Settled = false
	case "agent_settled":
		d.state.Streaming = false
		d.state.Settled = true
	case "tool_execution_start":
		id, _ := v["toolCallId"].(string)
		name, _ := v["toolName"].(string)
		d.tools[id] = name
		d.state.CurrentTool = name
		d.state.ParentToolCallID, _ = v["parentToolCallId"].(string)
	case "tool_execution_end":
		id, _ := v["toolCallId"].(string)
		delete(d.tools, id)
		d.state.CurrentTool = ""
		d.state.ParentToolCallID = ""
	}
	d.mu.Unlock()
	ev := Event{Kind: typ, Data: v, At: time.Now()}
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
func (d *RPC) Stop(ctx context.Context) error {
	d.mu.Lock()
	cmd := d.cmd
	stdin := d.stdin
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
	// Even an already-cancelled caller cannot skip SIGTERM's grace period.
	if wait(3*time.Second, false) {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if wait(2*time.Second, false) {
		return nil
	}
	return errors.New("pi group did not exit after SIGKILL")
}

var _ Driver = (*RPC)(nil)
