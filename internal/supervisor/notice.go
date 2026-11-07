package supervisor

import (
	"log"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

// SendMachineNotice delivers an urgent, daemon-authored notice (for example a
// low-disk warning) to every agent this daemon runs. Started agents receive it
// as an urgent pi prompt; attached agents receive it as a session signal.
func (m *Manager) SendMachineNotice(text string) {
	m.mu.Lock()
	var drivers []pidriver.Driver
	var names []string
	for _, a := range m.agents {
		if a.def.Kind == "attached" {
			m.sendAttachedSignal(a, "nudge", text)
			continue
		}
		if a.driver != nil && !a.machineStopped {
			drivers = append(drivers, a.driver)
			names = append(names, a.def.Name)
		}
	}
	m.mu.Unlock()
	for i, d := range drivers {
		if err := d.Send(pidriver.Outgoing{Text: text, Kind: pidriver.Urgent, Source: "machine-notice"}); err != nil {
			log.Printf("supervisor: machine notice %s: %v", names[i], err)
		}
	}
}
