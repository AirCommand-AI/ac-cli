package app

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	hookSourceClaudeCode = "claude-code"
	hookStateRepeatAfter = 60 * time.Second
)

// hookDir holds per-agent throttle and per-session identity notes.
func (a *App) hookDir() string { return filepath.Join(a.Store.Home(), ".aircommand", "hooks") }

type hookProcess struct {
	pid, ppid int
	command   string
}

// hook is adapter plumbing for runtimes that call aircom on their own events
// (Claude Code hooks). It reports work state for the agent whose listener runs
// in the same runtime session, never prints, and never fails the runtime.
func (a *App) hook(arguments []string) error {
	if len(arguments) != 2 || arguments[0] != hookSourceClaudeCode || (arguments[1] != "working" && arguments[1] != "idle") {
		return &publicError{message: "Usage: aircom hook claude-code <working|idle>"}
	}
	state := arguments[1]
	if a.Store == nil {
		return nil
	}
	agent, code, ok := a.hookAgentFromTranscript(os.Stdin)
	if !ok {
		table, err := processTable()
		if err != nil {
			return nil
		}
		if agent, code, ok = hookListener(table, os.Getpid()); !ok {
			return nil
		}
	}
	if code == "" {
		code = a.onlyWorkstreamOf(agent)
	}
	if code == "" || !a.hookStateDue(agent, code, state) {
		return nil
	}
	_ = a.state([]string{"--workstream", code, "--agent", agent, "--source", hookSourceClaudeCode, state})
	return nil
}

func processTable() ([]hookProcess, error) {
	output, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var table []hookProcess
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPID != nil || errPPID != nil {
			continue
		}
		table = append(table, hookProcess{pid: pid, ppid: ppid, command: strings.Join(fields[2:], " ")})
	}
	return table, nil
}

// hookListener finds the aircom listener started inside the same Claude Code
// process as the hook: the nearest ancestor whose command names claude must
// also be an ancestor of exactly one listening agent.
func hookListener(table []hookProcess, self int) (agent, code string, ok bool) {
	byPID := make(map[int]hookProcess, len(table))
	for _, process := range table {
		byPID[process.pid] = process
	}
	ancestors := func(pid int) []int {
		var chain []int
		for seen := 0; pid > 1 && seen < 64; seen++ {
			process, found := byPID[pid]
			if !found {
				break
			}
			chain = append(chain, process.pid)
			pid = process.ppid
		}
		return chain
	}
	session := 0
	for _, pid := range ancestors(self) {
		if pid != self && isClaudeProcess(byPID[pid].command) {
			session = pid
			break
		}
	}
	if session == 0 {
		return "", "", false
	}
	for _, process := range table {
		listenerAgent, listenerCode, isListener := listenerArgs(process.command)
		if !isListener {
			continue
		}
		for _, pid := range ancestors(process.ppid) {
			if pid != session {
				continue
			}
			if ok && listenerAgent != agent {
				return "", "", false // two agents in one session: do not guess
			}
			agent, code, ok = listenerAgent, listenerCode, true
		}
	}
	return agent, code, ok
}

func isClaudeProcess(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	if base == "claude" {
		return true
	}
	return (base == "node" || base == "bun") && len(fields) > 1 && strings.Contains(fields[1], "claude")
}

// listenerArgs recognises "aircom join ... --listen" and "aircom listen ...".
func listenerArgs(command string) (agent, code string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) < 2 || filepath.Base(fields[0]) != "aircom" || (fields[1] != "listen" && fields[1] != "join") {
		return "", "", false
	}
	listening := fields[1] == "listen"
	for index := 2; index < len(fields); index++ {
		switch fields[index] {
		case "--listen":
			listening = true
		case "--agent":
			if index+1 < len(fields) {
				agent = fields[index+1]
			}
		case "--workstream":
			if index+1 < len(fields) {
				code = fields[index+1]
			}
		}
	}
	return agent, code, listening && agent != ""
}

func (a *App) onlyWorkstreamOf(agent string) string {
	code := ""
	for _, local := range a.Store.ListLocalAgents() {
		if local.AgentID != agent && local.AgentName != agent {
			continue
		}
		if code != "" && code != local.WorkstreamCode {
			return ""
		}
		code = local.WorkstreamCode
	}
	return code
}

// hookStateDue throttles per agent: a hook fires on every tool call, but the
// server only needs a change or a refresh once a minute.
func (a *App) hookStateDue(agent, code, state string) bool {
	path := filepath.Join(a.hookDir(), strings.NewReplacer("/", "_", "..", "_").Replace(code+"-"+agent)+".json")
	var last struct {
		State string    `json:"state"`
		At    time.Time `json:"at"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &last) == nil && last.State == state && time.Since(last.At) < hookStateRepeatAfter {
		return false
	}
	last.State, last.At = state, time.Now()
	if data, err := json.Marshal(last); err == nil && os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
	return true
}

// hookInput is the JSON Claude Code writes to a hook's standard input.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

var transcriptAircom = regexp.MustCompile(`aircom (?:join|listen|leave)(?:[^"\\\n]|\\")*`)

// hookAgentFromTranscript identifies the agent from the session's own record:
// the last aircom join/listen (or leave) command it ran. The answer is cached
// per session so the transcript is read at most once a minute until found.
func (a *App) hookAgentFromTranscript(stdin io.Reader) (agent, code string, ok bool) {
	var input hookInput
	if json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&input) != nil || input.SessionID == "" || input.TranscriptPath == "" {
		return "", "", false
	}
	cachePath := filepath.Join(a.hookDir(), "session-"+strings.NewReplacer("/", "_", "..", "_").Replace(input.SessionID)+".json")
	var cached struct {
		Agent     string    `json:"agent"`
		Code      string    `json:"code"`
		CheckedAt time.Time `json:"checkedAt"`
	}
	if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &cached) == nil && time.Since(cached.CheckedAt) < hookStateRepeatAfter {
		return cached.Agent, cached.Code, cached.Agent != ""
	}
	cached.Agent, cached.Code = lastTranscriptListener(input.TranscriptPath)
	cached.CheckedAt = time.Now()
	if data, err := json.Marshal(cached); err == nil && os.MkdirAll(filepath.Dir(cachePath), 0o700) == nil {
		_ = os.WriteFile(cachePath, data, 0o600)
	}
	return cached.Agent, cached.Code, cached.Agent != ""
}

func lastTranscriptListener(path string) (agent, code string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "aircom ") || !strings.Contains(line, `"tool_use"`) {
			continue
		}
		for _, command := range transcriptAircom.FindAllString(line, -1) {
			command = strings.ReplaceAll(command, `\"`, `"`)
			fields := strings.Fields(command)
			if fields[1] == "leave" {
				agent, code = "", ""
				continue
			}
			if listenerAgent, listenerCode, listening := listenerArgs(command); listening {
				agent, code = listenerAgent, listenerCode
			}
		}
	}
	return agent, code
}
