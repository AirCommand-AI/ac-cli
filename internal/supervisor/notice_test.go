package supervisor

import (
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestSendMachineNoticeReachesRunningAgents(t *testing.T) {
	running, stopped := pidriver.NewFake(), pidriver.NewFake()
	attached := &managed{def: AgentDefinition{Name: "lead", Kind: "attached"}}
	m := &Manager{agents: map[string]*managed{
		"eng-1": {def: AgentDefinition{Name: "eng-1"}, driver: running},
		"eng-2": {def: AgentDefinition{Name: "eng-2"}, driver: stopped, machineStopped: true},
		"eng-3": {def: AgentDefinition{Name: "eng-3"}},
		"lead":  attached,
	}}
	m.SendMachineNotice("disk full")
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"running agent gets one urgent prompt", len(running.Sent), 1},
		{"machine-stopped agent gets none", len(stopped.Sent), 0},
		{"attached agent gets a queued signal", len(attached.pendingSignals), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %d want %d", tt.got, tt.want)
			}
		})
	}
	if sent := running.Sent[0]; sent.Kind != pidriver.Urgent || sent.Source != "machine-notice" || sent.Text != "disk full" {
		t.Fatalf("sent=%+v", sent)
	}
}
