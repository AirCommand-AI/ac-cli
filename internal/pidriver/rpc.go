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
type historyRequest struct {
	since  string
	result chan historyResponse
}
type historyResponse struct {
	entries []json.RawMessage
	next    string
	err     error
}
type toolCall struct{ name, parent string }
type RPC struct {
	opts            Options
	mu              sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	ready           chan struct{}
	exited          chan Exit
	done            chan struct{}
	readDone        chan struct{}
	runDone         chan struct{}
	events          chan Event
	notify          chan struct{}
	writerNotify    chan struct{}
	writes          [][]byte
	starting        bool
	lines           [][]byte
	outgoing        []Outgoing
	state           Snapshot
	closed          bool
	stopping        bool
	compacting      bool
	seq             int
	pending         map[int]Outgoing
	historyRequests []historyRequest
	pendingHistory  map[int]chan historyResponse
	tools           map[string]toolCall
	currentToolID   string
	interruptID     int
	interruptPhase  string
	interruptMsg    Outgoing
	restore         []string
}

func New(opts Options) Driver {
	return &RPC{opts: opts, ready: make(chan struct{}), exited: make(chan Exit, 1), done: make(chan struct{}), readDone: make(chan struct{}), runDone: make(chan struct{}), events: make(chan Event, 256), notify: make(chan struct{}, 1), writerNotify: make(chan struct{}, 1), pending: make(map[int]Outgoing), pendingHistory: make(map[int]chan historyResponse), tools: make(map[string]toolCall)}
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

const piVersionTimeout = 60 * time.Second

func checkVersion(path string) error {
	// A restored snapshot can make the first disk read unusually slow.
	ctx, cancel := context.WithTimeout(context.Background(), piVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pi --version: %w: %s", err, out)
	}
	return validateVersion(string(out))
}
func validateVersion(version string) error {
	m := versionRE.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("unrecognized pi version: %q", version)
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
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = spec.WorkDir
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
	d.cmd = cmd
	d.stdin = stdin
	d.state.PID = cmd.Process.Pid
	d.state.PGID = cmd.Process.Pid
	d.state.StartTime = processStartTime(cmd.Process.Pid)
	// Cmdline is informational only: node changes process.title after launch.
	// Populate it from /proc when pi confirms readiness, never use it as a kill fence.
	d.mu.Unlock()
	go func() { defer stdout.Close(); d.read(stdout) }()
	go d.write()
	go d.run()
	go func() {
		err := cmd.Wait()
		// A tool may have left descendants holding stdout open. Kill the group
		// after the parent exits, then drain final records with a fixed bound.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-d.readDone:
		case <-time.After(2 * time.Second):
			_ = stdout.Close()
			<-d.readDone
		}
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
		d.wake()
		select {
		case d.writerNotify <- struct{}{}:
		default:
		}
		<-d.runDone
		d.reportUndelivered()
		d.exited <- exit
		close(d.done)
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
	defer close(d.readDone)
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
func (d *RPC) History(ctx context.Context, since string, limit int) ([]json.RawMessage, string, error) {
	if limit <= 0 {
		return nil, since, nil
	}
	result := make(chan historyResponse, 1)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, "", errors.New("pi exited")
	}
	d.historyRequests = append(d.historyRequests, historyRequest{since, result})
	d.mu.Unlock()
	d.wake()
	select {
	case response := <-result:
		if len(response.entries) > limit {
			response.entries = response.entries[len(response.entries)-limit:]
		}
		if len(response.entries) > 0 {
			var entry struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(response.entries[len(response.entries)-1], &entry)
			response.next = entry.ID
		}
		return response.entries, response.next, response.err
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-d.done:
		return nil, "", errors.New("pi exited")
	}
}
func (d *RPC) Send(msg Outgoing) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.stopping {
		return errors.New("pi exited or stopping")
	}
	d.outgoing = append(d.outgoing, msg)
	d.wake()
	return nil
}
func (d *RPC) run() {
	defer close(d.runDone)
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
			if len(d.historyRequests) == 0 || !d.state.Ready || d.closed {
				d.mu.Unlock()
				break
			}
			req := d.historyRequests[0]
			d.historyRequests = d.historyRequests[1:]
			d.mu.Unlock()
			fields := map[string]any{}
			if req.since != "" {
				fields["since"] = req.since
			}
			id := d.command("get_entries", fields)
			d.mu.Lock()
			d.pendingHistory[id] = req.result
			d.mu.Unlock()
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
	if v["success"] != true {
		d.mu.Lock()
		restored := make([]Outgoing, 0, len(d.restore)+1)
		reason, _ := v["error"].(string)
		compact := strings.Contains(strings.ToLower(reason), "compact")
		if compact {
			restored = append(restored, d.interruptMsg)
		}
		for _, text := range d.restore {
			restored = append(restored, Outgoing{Text: text, Kind: Regular})
		}
		d.outgoing = append(restored, d.outgoing...)
		d.restore = nil
		d.interruptPhase = ""
		d.compacting = d.compacting || compact
		clock := d.opts.Clock
		if clock == nil {
			clock = time.Now
		}
		d.mu.Unlock()
		if !compact {
			d.emit(Event{Kind: "rpc_error", Data: v, At: clock()})
		}
		d.wake()
		return
	}
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
		d.pending[id] = msg
		d.interruptID = id
		d.interruptPhase = "prompt"
		d.mu.Unlock()
	case "prompt":
		d.mu.Lock()
		restored := make([]Outgoing, 0, len(d.restore))
		for _, text := range d.restore {
			restored = append(restored, Outgoing{Text: text, Kind: Regular})
		}
		d.outgoing = append(restored, d.outgoing...)
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
	if idstr, ok := v["id"].(string); ok && typ == "response" {
		id, _ := strconv.Atoi(idstr)
		d.mu.Lock()
		history := d.pendingHistory[id]
		delete(d.pendingHistory, id)
		d.mu.Unlock()
		if history != nil {
			response := historyResponse{}
			if v["success"] != true {
				response.err = fmt.Errorf("get_entries: %v", v["error"])
			} else if data, ok := v["data"].(map[string]any); ok {
				if raw, err := json.Marshal(data["entries"]); err == nil {
					_ = json.Unmarshal(raw, &response.entries)
				}
			}
			history <- response
		}
		d.mu.Lock()
		interruptResponse := d.interruptPhase != "" && id == d.interruptID
		d.mu.Unlock()
		if interruptResponse {
			if v["success"] != true {
				d.mu.Lock()
				delete(d.pending, id)
				d.mu.Unlock()
			}
			d.advanceInterrupt(v)
			if v["success"] != true {
				return
			}
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
				if data, ok := v["data"].(map[string]any); ok {
					if streaming, ok := data["isStreaming"].(bool); ok {
						d.state.Streaming = streaming
						d.state.Settled = !streaming
					}
				}
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
		parent, _ := v["parentToolCallId"].(string)
		d.tools[id] = toolCall{name, parent}
		d.currentToolID = id
		d.state.CurrentTool = name
		d.state.ParentToolCallID = parent
	case "tool_execution_end":
		id, _ := v["toolCallId"].(string)
		call := d.tools[id]
		delete(d.tools, id)
		if id == d.currentToolID {
			d.currentToolID = call.parent
			if parent, ok := d.tools[call.parent]; ok {
				d.state.CurrentTool = parent.name
				d.state.ParentToolCallID = parent.parent
			} else {
				d.state.CurrentTool = ""
				d.state.ParentToolCallID = ""
			}
		}
	}
	d.mu.Unlock()
	ev := Event{Kind: typ, Data: v, At: now()}
	d.emit(ev)
}
func (d *RPC) emit(ev Event) {
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
func (d *RPC) reportUndelivered() {
	d.mu.Lock()
	remaining := append([]Outgoing(nil), d.outgoing...)
	for _, msg := range d.pending {
		remaining = append(remaining, msg)
	}
	if d.interruptPhase != "" {
		remaining = append(remaining, d.interruptMsg)
	}
	d.mu.Unlock()
	clock := d.opts.Clock
	if clock == nil {
		clock = time.Now
	}
	for _, msg := range remaining {
		d.emit(Event{Kind: "undelivered", Data: map[string]any{"text": msg.Text, "kind": msg.Kind, "source": msg.Source}, At: clock()})
	}
}
func (d *RPC) Stop(ctx context.Context) error {
	d.mu.Lock()
	cmd := d.cmd
	stdin := d.stdin
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
