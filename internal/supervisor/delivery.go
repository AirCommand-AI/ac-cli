package supervisor

import (
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
	prefix := shellJoin([]string{m.CLI}) + " "
	base := prefix + "inbox --workstream " + shellJoin([]string{a.def.Workstream}) + " --agent " + shellJoin([]string{a.def.AgentID})
	send := prefix + "send --workstream " + shellJoin([]string{a.def.Workstream}) + " --agent " + shellJoin([]string{a.def.AgentID}) + " --to " + shellJoin([]string{n.SenderID}) + " --body <shell-quoted-reply>"
	ack := prefix + "ack --workstream " + shellJoin([]string{a.def.Workstream}) + " --agent " + shellJoin([]string{a.def.AgentID}) + " --message " + shellJoin([]string{n.MessageID})
	text := fmt.Sprintf("%s\nPointer only, no body. messageId=%q senderId=%q\nFetch: %s\nReply: %s\nThen ack, only after the action and reply both succeed: %s", agentapi.ComposeSummary(n, a.def.Workstream, nil), n.MessageID, n.SenderID, base, send, ack)
	d := a.driver
	m.mu.Unlock()
	err := d.Send(pidriver.Outgoing{Text: text, Kind: kind, Source: n.MessageID})
	m.mu.Lock()
	return err
}
