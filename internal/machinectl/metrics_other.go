//go:build !linux

package machinectl

import "golang.org/x/sys/unix"

// systemHost on macOS reports total memory only; disk comes from statfs.
type systemHost struct{}

func (systemHost) memory() (*MemoryMetric, *SwapMetric) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return nil, nil
	}
	return &MemoryMetric{TotalBytes: total}, nil
}

func (systemHost) load() []float64       { return nil }
func (systemHost) uptime() *int64        { return nil }
func (systemHost) cpu() (cpuTimes, bool) { return cpuTimes{}, false }
