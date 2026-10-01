package supervisor

import (
	"context"
	"fmt"
	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func (m *Manager) deliver(a *managed, n agentapi.Notification) error {
	if a.def.Mode != "headless" {
		return nil
	}
	if a.driver == nil || !a.startupSent {
		a.pendingWakes = append(a.pendingWakes, n)
		return nil
	}
	kind := pidriver.Regular
	if n.Priority == "urgent" {
		kind = pidriver.Urgent
	}
	text := pidriver.FormatMessageGuidance(m.CLI, a.def.Workstream, a.def.AgentID, agentapi.ComposeSummary(n, a.def.Workstream, nil), n.MessageID, n.SenderID)
	d := a.driver
	def := a.def
	m.mu.Unlock()
	var err error
	if n.Kind == "interrupt" {
		kind = pidriver.Interrupt
		if reader, ok := m.Poll.(interface {
			MessageBody(context.Context, AgentDefinition, string) (string, error)
		}); ok {
			text, err = reader.MessageBody(context.Background(), def, n.MessageID)
		} else {
			err = fmt.Errorf("interrupt body API unavailable")
		}
	}
	if err == nil {
		err = d.Send(pidriver.Outgoing{Text: text, Kind: kind, Source: n.MessageID})
	}
	m.mu.Lock()
	if err != nil && a.driver == d && a.def.Desired == "running" {
		a.pendingWakes = append(a.pendingWakes, n)
	}
	return nil
}
