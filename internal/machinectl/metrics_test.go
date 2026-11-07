package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantTotal uint64
		wantAvail *uint64
		wantSwap  *SwapMetric
	}{
		{
			name:      "full",
			in:        "MemTotal:       65536000 kB\nMemFree:  1000 kB\nMemAvailable:   32768000 kB\nSwapTotal:      33554432 kB\nSwapFree:       33554000 kB\n",
			wantTotal: 65536000 * 1024,
			wantAvail: ptr(uint64(32768000 * 1024)),
			wantSwap:  &SwapMetric{TotalBytes: 33554432 * 1024, UsedBytes: 432 * 1024},
		},
		{name: "no available or swap", in: "MemTotal: 1024 kB\n", wantTotal: 1024 * 1024},
		{name: "swap free above total is ignored", in: "MemTotal: 1 kB\nSwapTotal: 1 kB\nSwapFree: 2 kB\n", wantTotal: 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem, swap := parseMeminfo([]byte(tt.in))
			if mem == nil || mem.TotalBytes != tt.wantTotal {
				t.Fatalf("mem=%+v", mem)
			}
			if (mem.AvailableBytes == nil) != (tt.wantAvail == nil) || (tt.wantAvail != nil && *mem.AvailableBytes != *tt.wantAvail) {
				t.Errorf("available=%v want %v", mem.AvailableBytes, tt.wantAvail)
			}
			if (swap == nil) != (tt.wantSwap == nil) || (swap != nil && *swap != *tt.wantSwap) {
				t.Errorf("swap=%+v want %+v", swap, tt.wantSwap)
			}
		})
	}
	if mem, swap := parseMeminfo([]byte("garbage")); mem != nil || swap != nil {
		t.Errorf("garbage parsed: %+v %+v", mem, swap)
	}
}

func TestParseLoadUptimeStat(t *testing.T) {
	tests := []struct {
		name string
		run  func() bool
	}{
		{"loadavg", func() bool {
			l := parseLoadavg([]byte("3.50 2.25 1.00 4/900 12345\n"))
			return len(l) == 3 && l[0] == 3.5 && l[2] == 1
		}},
		{"loadavg short", func() bool { return parseLoadavg([]byte("1.0")) == nil }},
		{"uptime", func() bool { u := parseUptime([]byte("16133.26 50000.00\n")); return u != nil && *u == 16133 }},
		{"uptime empty", func() bool { return parseUptime(nil) == nil }},
		{"stat", func() bool {
			// user nice system idle iowait irq softirq steal guest guest_nice
			c, ok := parseProcStat([]byte("cpu  100 0 50 800 50 0 0 0 999 999\ncpu0 1 2 3 4\n"))
			return ok && c.total == 1000 && c.idle == 850
		}},
		{"stat wrong line", func() bool { _, ok := parseProcStat([]byte("intr 1 2 3 4 5")); return !ok }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.run() {
				t.Fail()
			}
		})
	}
}

func TestCPUPercent(t *testing.T) {
	tests := []struct {
		name      string
		prev, now cpuTimes
		want      float64
		ok        bool
	}{
		{"first sample", cpuTimes{}, cpuTimes{idle: 10, total: 100}, 0, false},
		{"half busy", cpuTimes{idle: 100, total: 200}, cpuTimes{idle: 150, total: 300}, 50, true},
		{"no progress", cpuTimes{idle: 1, total: 10}, cpuTimes{idle: 1, total: 10}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cpuPercent(tt.prev, tt.now)
			if ok != tt.ok || got != tt.want {
				t.Errorf("got %v,%v want %v,%v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDirSizeAndWalk(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "eng-1")
	writeFile(t, filepath.Join(a, "target", "big"), 3000)
	writeFile(t, filepath.Join(a, "src", "main.rs"), 100)
	if err := os.Symlink("/", filepath.Join(a, "loop")); err != nil {
		t.Fatal(err)
	}
	size, partial := dirSize(context.Background(), a)
	if size != 3100 || partial {
		t.Fatalf("size=%d partial=%v", size, partial)
	}
	if _, partial := dirSize(context.Background(), filepath.Join(root, "missing")); !partial {
		t.Error("missing dir should be partial")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, partial := dirSize(ctx, a); !partial {
		t.Error("cancelled walk should be partial")
	}

	c := NewMetricsCollector(root, func(context.Context) []AgentDir { return []AgentDir{{AgentID: "agm_1", Path: a}} })
	c.walkOnce(context.Background())
	m := c.Collect()
	if len(m.AgentDirs) != 1 || m.AgentDirs[0].AgentID != "agm_1" || m.AgentDirs[0].Bytes != 3100 || m.AgentDirs[0].MeasuredAt == "" {
		t.Fatalf("agentDirs=%+v", m.AgentDirs)
	}
	if len(m.Disk) == 0 || m.Disk[0].Mount != "/" || m.Disk[0].TotalBytes == 0 {
		t.Fatalf("disk=%+v", m.Disk)
	}
	if m.Memory == nil || m.Memory.TotalBytes == 0 {
		t.Fatalf("memory=%+v", m.Memory)
	}
}

type fakeHost struct{ times []cpuTimes }

func (f *fakeHost) memory() (*MemoryMetric, *SwapMetric) { return &MemoryMetric{TotalBytes: 8}, nil }
func (f *fakeHost) load() []float64                      { return []float64{1, 2, 3} }
func (f *fakeHost) uptime() *int64                       { return ptr(int64(60)) }
func (f *fakeHost) cpu() (cpuTimes, bool) {
	t := f.times[0]
	f.times = f.times[1:]
	return t, true
}

func TestCollectCPUAcrossSamples(t *testing.T) {
	c := &MetricsCollector{host: &fakeHost{times: []cpuTimes{{idle: 0, total: 100}, {idle: 25, total: 200}}}}
	if m := c.Collect(); m.CPUPercent != nil {
		t.Fatalf("first sample cpu=%v", *m.CPUPercent)
	}
	m := c.Collect()
	if m.CPUPercent == nil || *m.CPUPercent != 75 || len(m.Load) != 3 || *m.UptimeSec != 60 {
		t.Fatalf("sample=%+v", m)
	}
}

type staticMetrics struct{ m Metrics }

func (s staticMetrics) Collect() Metrics { return s.m }

func TestHTTPReporterSendsMetrics(t *testing.T) {
	tests := []struct {
		name    string
		metrics interface{ Collect() Metrics }
		want    bool
	}{
		{"with collector", staticMetrics{Metrics{Disk: []DiskMetric{{Mount: "/", TotalBytes: 100, AvailBytes: 4}}}}, true},
		{"without collector", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				raw, ok := body["metrics"]
				if ok != tt.want {
					t.Errorf("metrics present=%v want %v", ok, tt.want)
				}
				if ok {
					var m Metrics
					if err := json.Unmarshal(raw, &m); err != nil || len(m.Disk) != 1 || m.Disk[0].AvailBytes != 4 {
						t.Errorf("metrics=%s err=%v", raw, err)
					}
				}
				_, _ = w.Write([]byte(`{"machine":{"state":"online"}}`))
			}))
			defer server.Close()
			r := &HTTPReporter{URL: server.URL, Token: "machine", Source: &fakeStatusSource{}, Metrics: tt.metrics}
			if err := r.Report(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }
