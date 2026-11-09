package agentstate

import (
	"testing"
	"time"
)

func TestTransitions(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, program string
		events        []Event
		physical      Physical
		logical       Logical
		reason        string
	}{
		{"pi closed", "pi", nil, Stopped, "", "pi closed"},
		{"live idle", "pi", []Event{{Kind: "session_alive"}}, Running, Idle, ""},
		{"run working", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}}, Running, Working, ""},
		{"turn refreshes", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "turn", At: base.Add(10 * time.Minute)}, {Kind: "tick", At: base.Add(14 * time.Minute)}}, Running, Working, ""},
		{"tool refreshes", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tool_start", At: base.Add(10 * time.Minute)}, {Kind: "tool_end", At: base.Add(20 * time.Minute)}}, Running, Working, ""},
		{"silent model", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tick", At: base.Add(5*time.Minute + time.Second)}}, Running, Stuck, "no model response 5m"},
		{"long tool with progress", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tool_start", Tool: "bash", Command: "cargo test"}, {Kind: "tool_progress", At: base.Add(20 * time.Minute)}, {Kind: "tick", At: base.Add(24 * time.Minute)}}, Running, Working, ""},
		{"idle tool", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tool_start", Tool: "bash", Command: "npm install"}, {Kind: "tick", At: base.Add(6 * time.Minute)}}, Running, Stuck, "tool idle 5m (bash: npm install)"},
		{"disk full", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "disk_full"}}, Running, Stuck, "disk full"},
		{"disk freed", "pi", []Event{{Kind: "session_alive"}, {Kind: "disk_full"}, {Kind: "disk_ok"}}, Running, Idle, ""},
		{"activity returns", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tick", At: base.Add(6 * time.Minute)}, {Kind: "rpc", At: base.Add(7 * time.Minute)}}, Running, Working, ""},
		{"run ends", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "run_end", At: base.Add(time.Minute)}}, Running, Idle, ""},
		{"approval priority", "pi", []Event{{Kind: "session_alive"}, {Kind: "task_in_flight", TaskNumber: "13"}, {Kind: "task_blocked", TaskNumber: "12"}, {Kind: "approval_pending", TaskNumber: "6"}}, Running, Waiting, "approval for #6"},
		{"blocked task", "pi", []Event{{Kind: "session_alive"}, {Kind: "task_blocked", TaskNumber: "12"}}, Running, Waiting, "#12 blocked"},
		{"in flight", "pi", []Event{{Kind: "session_alive"}, {Kind: "task_in_flight", TaskNumber: "13"}}, Running, Waiting, "#13 in flight"},
		{"clear", "pi", []Event{{Kind: "session_alive"}, {Kind: "task_in_flight", TaskNumber: "13"}, {Kind: "task_clear"}}, Running, Idle, ""},
		{"other unknown", "other", []Event{{Kind: "session_alive"}, {Kind: "run_start"}}, Running, Unknown, ""},
		{"other explicit", "other", []Event{{Kind: "session_alive"}, {Kind: "state", Logical: Working}}, Running, Working, ""},
		{"other stream ends", "other", []Event{{Kind: "session_alive"}, {Kind: "no_listener"}}, Stopped, "", "no listener"},
		{"pi exited", "pi", []Event{{Kind: "session_alive"}, {Kind: "session_exited"}}, Stopped, "", "pi closed"},
		{"crash", "pi", []Event{{Kind: "session_alive"}, {Kind: "crashed"}}, Stopped, "", "crashed"},
		{"machine stopping", "pi", []Event{{Kind: "session_alive"}, {Kind: "machine_stopping"}}, Stopped, "", "machine stopping"},
		{"dashboard stop", "pi", []Event{{Kind: "session_alive"}, {Kind: "dashboard_stop"}}, Stopped, "", "stopped from the dashboard"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.program, base)
			for _, e := range tc.events {
				if e.At.IsZero() {
					e.At = base.Add(time.Second)
				}
				s, _ = Step(s, e)
			}
			if s.State.Physical != tc.physical || s.State.Logical != tc.logical || s.State.Reason != tc.reason {
				t.Fatalf("got %+v", s.State)
			}
		})
	}
}

func TestStuckAndModelHangOncePerEpisode(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := New("pi", base)
	s, _ = Step(s, Event{Kind: "session_alive", At: base})
	s, _ = Step(s, Event{Kind: "run_start", At: base})
	s, n := Step(s, Event{Kind: "tool_start", Tool: "bash", Command: "make", At: base})
	if s.State.Logical != Working || s.State.Tool.Label() != "bash: make" || n != NoNudge {
		t.Fatalf("working with tool: %+v %s", s.State, n)
	}
	s, n = Step(s, Event{Kind: "tick", At: base.Add(6 * time.Minute)})
	if s.State.Logical != Stuck || n != NoNudge || s.State.Tool.Name != "" {
		t.Fatalf("idle tool is stuck, without nudge: %+v %s", s.State, n)
	}
	s, _ = Step(s, Event{Kind: "tool_end", At: base.Add(7 * time.Minute)})
	s, n = Step(s, Event{Kind: "tick", At: base.Add(12*time.Minute + time.Second)})
	if s.State.Logical != Stuck || s.State.Reason != "no model response 5m" || n != NudgeModelHang {
		t.Fatalf("model hang: %+v %s", s.State, n)
	}
	since := s.State.Since
	s, n = Step(s, Event{Kind: "tick", At: base.Add(13 * time.Minute)})
	if n != NoNudge || !s.State.Since.Equal(since) {
		t.Fatalf("repeat or churn: %+v %s", s.State, n)
	}
	// The interrupted run ends and the resent message hangs again: no second interrupt.
	s, _ = Step(s, Event{Kind: "run_end", At: base.Add(14 * time.Minute)})
	s, _ = Step(s, Event{Kind: "run_start", At: base.Add(14 * time.Minute)})
	_, n = Step(s, Event{Kind: "tick", At: base.Add(20 * time.Minute)})
	if n != NoNudge {
		t.Fatalf("second hang in a row: %s", n)
	}
	// Waiting on an in-flight task no longer nudges (the dashboard does).
	w := New("pi", base)
	w, _ = Step(w, Event{Kind: "session_alive", At: base})
	w, _ = Step(w, Event{Kind: "task_in_flight", TaskNumber: "7", At: base})
	if _, n = Step(w, Event{Kind: "tick", At: base.Add(time.Hour)}); n != NoNudge {
		t.Fatalf("in-flight nudge: %s", n)
	}
}

func TestReportSchedule(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := New("pi", base).State
	if !Due(s, s, time.Time{}, base) || Due(s, s, base, base.Add(24*time.Minute)) || !Due(s, s, base, base.Add(25*time.Minute)) {
		t.Fatal("25-minute report contract")
	}
}
