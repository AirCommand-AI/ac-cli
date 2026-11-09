package supervisor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Tool liveness and disk checks (design-team-watch): a working agent's tool
// counts as alive while its processes use CPU or do I/O.
const (
	sampleEvery    = 30 * time.Second
	commandMax     = 80
	diskFullBytes  = 1 << 30
	diskFullFactor = 0.02
)

// toolOf returns a tool event's name and, for shell tools, its command
// shortened to one line of at most 80 characters.
func toolOf(data map[string]any) (string, string) {
	name, _ := data["toolName"].(string)
	if name == "" {
		return "", ""
	}
	if !strings.EqualFold(name, "bash") {
		return name, ""
	}
	args, _ := data["args"].(map[string]any)
	command, _ := args["command"].(string)
	command = strings.Join(strings.Fields(command), " ")
	if utf8.RuneCountInString(command) > commandMax {
		command = string([]rune(command)[:commandMax-1]) + "…"
	}
	return name, command
}

// toolCounters sums CPU ticks and I/O bytes of every descendant of root.
// ok is false when nothing could be read (no descendants, or unsupported).
func toolCounters(root int) (cpu, io uint64, ok bool) {
	if root <= 0 {
		return 0, 0, false
	}
	if runtime.GOOS != "linux" {
		return psCounters(root)
	}
	for _, pid := range descendants(root) {
		if c, ok := procCPU(pid); ok {
			cpu += c
			ok = true
		}
		io += procIO(pid)
	}
	return cpu, io, cpu > 0 || io > 0
}

// descendants walks /proc/<pid>/task/*/children.
func descendants(root int) []int {
	var out []int
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		tasks, _ := filepath.Glob("/proc/" + strconv.Itoa(pid) + "/task/*/children")
		for _, t := range tasks {
			data, err := os.ReadFile(t)
			if err != nil {
				continue
			}
			for _, f := range strings.Fields(string(data)) {
				if child, err := strconv.Atoi(f); err == nil {
					out = append(out, child)
					queue = append(queue, child)
				}
			}
		}
		if len(out) > 4096 {
			break
		}
	}
	return out
}

func procCPU(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

func procIO(pid int) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0
	}
	var total uint64
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "rchar" && key != "wchar" {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
			total += n
		}
	}
	return total
}

// psCounters is the macOS fallback: CPU time only, from ps.
func psCounters(root int) (uint64, uint64, bool) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,time=").Output()
	if err != nil {
		return 0, 0, false
	}
	children := map[int][]int{}
	cpu := map[int]uint64{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		cpu[pid] = psTime(f[2])
	}
	var total uint64
	found := false
	queue := append([]int(nil), children[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		total += cpu[pid]
		found = true
		queue = append(queue, children[pid]...)
	}
	return total, 0, found
}

// psTime parses ps TIME ([[dd-]hh:]mm:ss.cc) into hundredths of a second.
func psTime(v string) uint64 {
	var total float64
	days := 0.0
	if d, rest, ok := strings.Cut(v, "-"); ok {
		days, _ = strconv.ParseFloat(d, 64)
		v = rest
	}
	for _, part := range strings.Split(v, ":") {
		n, _ := strconv.ParseFloat(part, 64)
		total = total*60 + n
	}
	return uint64((total + days*86400) * 100)
}

// diskFull reports whether the filesystem holding path is nearly full.
func diskFull(path string) (bool, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, false
	}
	size := uint64(st.Blocks) * uint64(st.Bsize)
	avail := uint64(st.Bavail) * uint64(st.Bsize)
	if size == 0 {
		return false, false
	}
	return avail < diskFullBytes || float64(avail) < float64(size)*diskFullFactor, true
}

// sampleTool emits tool_progress when the running tool's processes used CPU
// or did I/O since the last sample. Called with m.mu held from the tick.
func (m *Manager) sampleTool(a *managed) {
	now := m.now()
	if a.presence.Tool.Name == "" || a.def.Pi == nil || now.Before(a.nextSample) {
		return
	}
	a.nextSample = now.Add(sampleEvery)
	cpu, io, ok := toolCounters(a.def.Pi.PID)
	if !ok {
		return
	}
	if cpu != a.lastToolCPU || io != a.lastToolIO {
		a.lastToolCPU, a.lastToolIO = cpu, io
		m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: "tool_progress", At: now})
	}
}

// checkDisk tells every started agent when the machine's disk fills or frees.
// Called with m.mu held from the tick.
func (m *Manager) checkDisk() {
	now := m.now()
	if now.Before(m.nextDiskCheck) {
		return
	}
	m.nextDiskCheck = now.Add(sampleEvery)
	full, ok := diskFull(m.Home)
	if !ok || full == m.diskFull {
		return
	}
	m.diskFull = full
	kind := "disk_ok"
	if full {
		kind = "disk_full"
	}
	for _, a := range m.agents {
		if a.def.Kind != "attached" {
			m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: kind, At: now})
		}
	}
}
