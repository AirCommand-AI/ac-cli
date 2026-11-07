package machinectl

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DiskCriticalPercent is the available-space share below which every agent
	// on the machine is told to free space.
	DiskCriticalPercent = 5.0
	// DiskAlertRearm is how long space must stay above the threshold before
	// another notice can be sent.
	DiskAlertRearm = 10 * time.Minute
)

// DiskAlert wraps a collector and sends one notice per low-disk episode.
type DiskAlert struct {
	Collector interface {
		Collect() Metrics
		AgentName(string) string
	}
	Notice func(string)
	Now    func() time.Time

	mu       sync.Mutex
	alerted  bool
	clearFor time.Time
}

func (d *DiskAlert) Collect() Metrics {
	m := d.Collector.Collect()
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	low, ok := lowestDisk(m.Disk)
	d.mu.Lock()
	send := false
	switch {
	case !ok:
	case availPercent(low) < DiskCriticalPercent:
		d.clearFor = time.Time{}
		if !d.alerted {
			d.alerted, send = true, true
		}
	case d.alerted && d.clearFor.IsZero():
		d.clearFor = now
	case d.alerted && now.Sub(d.clearFor) >= DiskAlertRearm:
		d.alerted, d.clearFor = false, time.Time{}
	}
	d.mu.Unlock()
	if send && d.Notice != nil {
		d.Notice(d.text(low, m.AgentDirs))
	}
	return m
}

func (d *DiskAlert) text(disk DiskMetric, dirs []AgentDirMetric) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Machine notice: disk %s is %.0f%% full (%s left of %s). When it fills, every agent on this machine stops working.", disk.Mount, 100-availPercent(disk), gb(disk.AvailBytes), gb(disk.TotalBytes))
	sorted := append([]AgentDirMetric(nil), dirs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Bytes > sorted[j].Bytes })
	if len(sorted) > 0 {
		b.WriteString(" Work dir sizes:")
		for i, dir := range sorted {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, " %s %s", d.Collector.AgentName(dir.AgentID), gb(dir.Bytes))
		}
		b.WriteString(".")
	}
	b.WriteString(" Free space in your own work dir now (for example `cargo clean` or delete old build output), then report what you removed.")
	return b.String()
}

func lowestDisk(disks []DiskMetric) (DiskMetric, bool) {
	var low DiskMetric
	found := false
	for _, disk := range disks {
		if disk.TotalBytes == 0 {
			continue
		}
		if !found || availPercent(disk) < availPercent(low) {
			low, found = disk, true
		}
	}
	return low, found
}

func availPercent(d DiskMetric) float64 {
	if d.TotalBytes == 0 {
		return 100
	}
	return float64(d.AvailBytes) * 100 / float64(d.TotalBytes)
}

func gb(bytes uint64) string {
	return fmt.Sprintf("%.0f GB", float64(bytes)/1e9)
}
