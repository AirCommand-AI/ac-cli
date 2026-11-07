package machinectl

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DirWalkEvery is how often agent work dirs are measured.
	DirWalkEvery = 10 * time.Minute
	// DirWalkBudget caps one pass over all agent dirs.
	DirWalkBudget = 60 * time.Second
)

// Metrics is the machine health sample sent with each status report. Every
// field is optional: a failed reading is omitted, never an error.
type Metrics struct {
	Disk       []DiskMetric     `json:"disk,omitempty"`
	Memory     *MemoryMetric    `json:"memory,omitempty"`
	Swap       *SwapMetric      `json:"swap,omitempty"`
	CPUPercent *float64         `json:"cpuPercent,omitempty"`
	Load       []float64        `json:"load,omitempty"`
	UptimeSec  *int64           `json:"uptimeSec,omitempty"`
	AgentDirs  []AgentDirMetric `json:"agentDirs,omitempty"`
}

type DiskMetric struct {
	Mount      string `json:"mount"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	AvailBytes uint64 `json:"availBytes"`
	InodesFree uint64 `json:"inodesFree"`
}

type MemoryMetric struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes *uint64 `json:"availableBytes,omitempty"`
}

type SwapMetric struct {
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

type AgentDirMetric struct {
	AgentID    string `json:"agentId"`
	Bytes      uint64 `json:"bytes"`
	Partial    bool   `json:"partial,omitempty"`
	MeasuredAt string `json:"measuredAt"`
}

// AgentDir names one agent's work dir for measurement.
type AgentDir struct {
	AgentID, Name, Path string
}

// MetricsCollector reads machine health without writing to disk, so it keeps
// working when the disk is full. Dir sizes are measured in the background.
type MetricsCollector struct {
	WorkRoot string
	// Dirs lists the agent work dirs to measure on each background pass.
	Dirs func(context.Context) []AgentDir
	host hostReader

	mu    sync.Mutex
	cpu   cpuTimes
	dirs  []AgentDirMetric
	names map[string]string
}

// NewMetricsCollector reads the host the daemon runs on.
func NewMetricsCollector(workRoot string, dirs func(context.Context) []AgentDir) *MetricsCollector {
	return &MetricsCollector{WorkRoot: workRoot, Dirs: dirs, host: systemHost{}}
}

// Collect returns a fresh sample; dir sizes come from the last background pass.
func (c *MetricsCollector) Collect() Metrics {
	var m Metrics
	m.Disk = c.disks()
	m.Memory, m.Swap = c.host.memory()
	m.Load = c.host.load()
	m.UptimeSec = c.host.uptime()
	if now, ok := c.host.cpu(); ok {
		c.mu.Lock()
		if pct, ok := cpuPercent(c.cpu, now); ok {
			m.CPUPercent = &pct
		}
		c.cpu = now
		c.mu.Unlock()
	}
	c.mu.Lock()
	m.AgentDirs = append([]AgentDirMetric(nil), c.dirs...)
	c.mu.Unlock()
	return m
}

func (c *MetricsCollector) disks() []DiskMetric {
	var out []DiskMetric
	root, rootErr := statDisk("/")
	if rootErr == nil {
		out = append(out, root)
	}
	if c.WorkRoot == "" {
		return out
	}
	work, err := statDisk(c.WorkRoot)
	if err != nil {
		return out
	}
	if rootErr == nil && sameFilesystem("/", c.WorkRoot) {
		return out
	}
	return append(out, work)
}

func statDisk(path string) (DiskMetric, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskMetric{}, err
	}
	block := uint64(st.Bsize)
	return DiskMetric{Mount: path, TotalBytes: st.Blocks * block, FreeBytes: st.Bfree * block, AvailBytes: st.Bavail * block, InodesFree: uint64(st.Ffree)}, nil
}

func sameFilesystem(a, b string) bool {
	var sa, sb unix.Stat_t
	if unix.Stat(a, &sa) != nil || unix.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

// RunDirWalker measures agent dirs now and then every DirWalkEvery.
func (c *MetricsCollector) RunDirWalker(ctx context.Context) {
	ticker := time.NewTicker(DirWalkEvery)
	defer ticker.Stop()
	for {
		c.walkOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *MetricsCollector) walkOnce(ctx context.Context) {
	if c.Dirs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, DirWalkBudget)
	defer cancel()
	var out []AgentDirMetric
	names := map[string]string{}
	for _, d := range c.Dirs(ctx) {
		names[d.AgentID] = d.Name
		bytes, partial := dirSize(ctx, d.Path)
		out = append(out, AgentDirMetric{AgentID: d.AgentID, Bytes: bytes, Partial: partial, MeasuredAt: time.Now().UTC().Format(time.RFC3339)})
	}
	c.mu.Lock()
	c.dirs, c.names = out, names
	c.mu.Unlock()
}

// AgentName returns the name recorded for an agent on the last dir pass.
func (c *MetricsCollector) AgentName(agentID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name := c.names[agentID]; name != "" {
		return name
	}
	return agentID
}

// dirSize sums regular file sizes without following symlinks. Partial is true
// when the walk ran out of time or hit unreadable entries.
func dirSize(ctx context.Context, root string) (uint64, bool) {
	var total uint64
	partial := false
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			partial = true
			return fs.SkipAll
		}
		if err != nil {
			partial = true
			return nil
		}
		if d.Type()&os.ModeType != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			partial = true
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	if err != nil {
		partial = true
	}
	return total, partial
}

type cpuTimes struct {
	idle, total uint64
}

func cpuPercent(prev, now cpuTimes) (float64, bool) {
	if prev.total == 0 || now.total <= prev.total {
		return 0, false
	}
	busy := float64((now.total - prev.total) - (now.idle - prev.idle))
	return busy * 100 / float64(now.total-prev.total), true
}

// hostReader isolates /proc and sysctl reads for tests.
type hostReader interface {
	memory() (*MemoryMetric, *SwapMetric)
	load() []float64
	uptime() *int64
	cpu() (cpuTimes, bool)
}
