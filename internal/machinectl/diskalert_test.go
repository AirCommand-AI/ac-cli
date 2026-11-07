package machinectl

import (
	"strings"
	"testing"
	"time"
)

type scriptedCollector struct {
	avail []uint64
	dirs  []AgentDirMetric
}

func (s *scriptedCollector) Collect() Metrics {
	a := s.avail[0]
	s.avail = s.avail[1:]
	return Metrics{Disk: []DiskMetric{{Mount: "/", TotalBytes: 1000e9, AvailBytes: a}}, AgentDirs: s.dirs}
}
func (s *scriptedCollector) AgentName(id string) string {
	return map[string]string{"agm_1": "eng-1", "agm_2": "eng-2"}[id]
}

func TestDiskAlertEpisodes(t *testing.T) {
	tests := []struct {
		name  string
		avail []uint64 // GB available out of 1000, one per 1-minute sample
		want  int      // notices sent
	}{
		{"healthy never alerts", []uint64{500, 200, 60}, 0},
		{"one notice per episode", []uint64{40, 30, 10, 0}, 1},
		{"brief recovery does not rearm", []uint64{40, 60, 60, 40}, 1},
		{"rearms after 10 minutes clear", []uint64{40, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 40}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			avail := make([]uint64, len(tt.avail))
			for i, v := range tt.avail {
				avail[i] = v * 1e9
			}
			now := time.Date(2026, 11, 7, 19, 0, 0, 0, time.UTC)
			var notices []string
			d := &DiskAlert{Collector: &scriptedCollector{avail: avail}, Notice: func(s string) { notices = append(notices, s) }, Now: func() time.Time { return now }}
			for range tt.avail {
				d.Collect()
				now = now.Add(time.Minute)
			}
			if len(notices) != tt.want {
				t.Fatalf("notices=%d want %d: %v", len(notices), tt.want, notices)
			}
		})
	}
}

func TestDiskAlertTextListsLargestDirsFirst(t *testing.T) {
	c := &scriptedCollector{avail: []uint64{20e9}, dirs: []AgentDirMetric{{AgentID: "agm_1", Bytes: 100e9}, {AgentID: "agm_2", Bytes: 300e9}}}
	var got string
	(&DiskAlert{Collector: c, Notice: func(s string) { got = s }}).Collect()
	for _, want := range []string{"disk / is 98% full (20 GB left of 1000 GB)", "Work dir sizes: eng-2 300 GB, eng-1 100 GB.", "cargo clean"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q missing %q", got, want)
		}
	}
}

func TestLowestDiskSkipsEmpty(t *testing.T) {
	low, ok := lowestDisk([]DiskMetric{{Mount: "/", TotalBytes: 100, AvailBytes: 50}, {Mount: "/x"}, {Mount: "/w", TotalBytes: 100, AvailBytes: 10}})
	if !ok || low.Mount != "/w" {
		t.Fatalf("low=%+v ok=%v", low, ok)
	}
	if _, ok := lowestDisk(nil); ok {
		t.Fatal("no disks should not be ok")
	}
}
