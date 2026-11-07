//go:build linux

package machinectl

import "os"

type systemHost struct{}

func (systemHost) memory() (*MemoryMetric, *SwapMetric) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, nil
	}
	return parseMeminfo(data)
}

func (systemHost) load() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	return parseLoadavg(data)
}

func (systemHost) uptime() *int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return nil
	}
	return parseUptime(data)
}

func (systemHost) cpu() (cpuTimes, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	return parseProcStat(data)
}
