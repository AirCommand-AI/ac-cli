package machinectl

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// Parsers for Linux /proc files; kept untagged so tests run on every OS.

func parseMeminfo(data []byte) (*MemoryMetric, *SwapMetric) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = kb * 1024
	}
	var mem *MemoryMetric
	if total, ok := values["MemTotal"]; ok {
		mem = &MemoryMetric{TotalBytes: total}
		if avail, ok := values["MemAvailable"]; ok {
			mem.AvailableBytes = &avail
		}
	}
	var swap *SwapMetric
	total, okTotal := values["SwapTotal"]
	free, okFree := values["SwapFree"]
	if okTotal && okFree && free <= total {
		swap = &SwapMetric{TotalBytes: total, UsedBytes: total - free}
	}
	return mem, swap
}

func parseLoadavg(data []byte) []float64 {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, f := range fields[:3] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func parseUptime(data []byte) *int64 {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil
	}
	sec := int64(v)
	return &sec
}

// parseProcStat reads the aggregate "cpu" line: idle includes iowait.
func parseProcStat(data []byte) (cpuTimes, bool) {
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		// guest and guest_nice (fields 9, 10) are already counted in user.
		if i >= 8 {
			break
		}
		t.total += v
		if i == 3 || i == 4 {
			t.idle += v
		}
	}
	return t, true
}
