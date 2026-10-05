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
		{"turn refreshes", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "turn", At: base.Add(10 * time.Minute)}, {Kind: "tick", At: base.Add(20 * time.Minute)}}, Running, Working, ""},
		{"tool refreshes", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tool_start", At: base.Add(10 * time.Minute)}, {Kind: "tool_end", At: base.Add(20 * time.Minute)}}, Running, Working, ""},
		{"quiet run", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tick", At: base.Add(15*time.Minute + time.Second)}}, Running, NoActivity, "no activity 15m"},
		{"activity returns", "pi", []Event{{Kind: "session_alive"}, {Kind: "run_start"}, {Kind: "tick", At: base.Add(16 * time.Minute)}, {Kind: "rpc", At: base.Add(17 * time.Minute)}}, Running, Working, ""},
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

func TestNudgesOncePerEpisodeAndSince(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := New("pi", base)
	s, _ = Step(s, Event{Kind: "session_alive", At: base})
	s, _ = Step(s, Event{Kind: "run_start", At: base})
	s, n := Step(s, Event{Kind: "tick", At: base.Add(15 * time.Minute)})
	if n != NoNudge || s.State.Logical != NoActivity || !s.State.Since.Equal(base.Add(15*time.Minute)) {
		t.Fatalf("first quiet: %+v %s", s, n)
	}
	s, n = Step(s, Event{Kind: "tick", At: base.Add(30 * time.Minute)})
	if n != NudgeNoActivity || !s.State.Since.Equal(base.Add(15*time.Minute)) {
		t.Fatalf("nudge: %+v %s", s, n)
	}
	_, n = Step(s, Event{Kind: "tick", At: base.Add(31 * time.Minute)})
	if n != NoNudge {
		t.Fatalf("repeat %s", n)
	}
	s, _ = Step(s, Event{Kind: "run_end", At: base.Add(32 * time.Minute)})
	s, _ = Step(s, Event{Kind: "task_in_flight", TaskNumber: "7", At: base.Add(33 * time.Minute)})
	s, n = Step(s, Event{Kind: "tick", At: base.Add(48 * time.Minute)})
	if n != NudgeInFlight {
		t.Fatalf("waiting nudge: %s", n)
	}
	s, n = Step(s, Event{Kind: "tick", At: base.Add(49 * time.Minute)})
	if n != NoNudge {
		t.Fatalf("repeat waiting %s", n)
	}
	s, _ = Step(s, Event{Kind: "approval_pending", TaskNumber: "6", At: base.Add(50 * time.Minute)})
	s, n = Step(s, Event{Kind: "tick", At: base.Add(70 * time.Minute)})
	if n != NoNudge {
		t.Fatalf("approval nudge %s", n)
	}
	s, _ = Step(s, Event{Kind: "approval_clear", At: base.Add(71 * time.Minute)})
	s, n = Step(s, Event{Kind: "tick", At: base.Add(86 * time.Minute)})
	if n != NudgeInFlight {
		t.Fatalf("fresh waiting episode: %s", n)
	}
}

func TestReportScheduleAndMachineFunctions(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := New("pi", base).State
	if !Due(s, s, time.Time{}, base) || Due(s, s, base, base.Add(24*time.Minute)) || !Due(s, s, base, base.Add(25*time.Minute)) {
		t.Fatal("25-minute report contract")
	}
	attached := MachineAgent{Kind: "attached", Physical: Running, InRun: true}
	busyIdle, lastBusy := MachineIdle(base, time.Time{}, time.Time{}, []MachineAgent{attached}, false)
	if !Busy([]MachineAgent{attached}, false) || busyIdle != nil || !lastBusy.Equal(base) {
		t.Fatal("attached run should keep machine busy and advance its marker")
	}
	attached.InRun = false
	idle := IdleSince(base, base.Add(-time.Hour), base.Add(-time.Minute), []MachineAgent{attached}, false)
	if idle == nil || !idle.Equal(base.Add(-time.Minute)) {
		t.Fatalf("idle = %v", idle)
	}
	if !AgentsStopped(true, []MachineAgent{attached}) || AgentsStopped(false, nil) || AgentsStopped(true, []MachineAgent{{Kind: "started"}}) {
		t.Fatal("shutdown should ignore attached sessions only")
	}
}
