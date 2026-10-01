package supervisor

import (
	"context"
	"errors"
	"fmt"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

// deliver runs under m.mu, releasing it around the fetch and the driver Send.
// A failed interrupt body fetch is bounded: after three attempts (or a
// permanent 4xx), the normal urgent pointer lets the agent read/ack it and
// keeps later wakes from being blocked behind it.
func (m *Manager) deliver(ctx context.Context, a *managed, n agentapi.Notification) error {
	if a.def.Mode != "headless" {
		return nil
	}
	if a.driver == nil || !a.startupSent || len(a.pendingWakes) != 0 {
		a.pendingWakes = append(a.pendingWakes, n)
		return nil
	}
	kind := pidriver.Regular
	if n.Priority == "urgent" {
		kind = pidriver.Urgent
	}
	guidance := pidriver.FormatMessageGuidance(m.CLI, a.def.Workstream, a.def.AgentID, agentapi.ComposeSummary(n, a.def.Workstream, nil), n.MessageID, n.SenderID)
	text := guidance
	d := a.driver
	def := a.def
	attempts := a.interruptFailures[n.MessageID]
	m.mu.Unlock()
	var err error
	fallback := false
	fetchFailed := false
	if n.Kind == "interrupt" {
		kind = pidriver.Interrupt
		if attempts >= 3 {
			fallback = true
		} else if reader, ok := m.Poll.(interface {
			MessageBody(context.Context, AgentDefinition, string) (string, error)
		}); ok {
			var body string
			body, err = reader.MessageBody(ctx, def, n.MessageID)
			if err == nil {
				text = guidance + "\nInterrupt body: " + body
			}
		} else {
			err = fmt.Errorf("interrupt body API unavailable")
		}
		if err != nil {
			fetchFailed = true
			var status *APIStatusError
			fallback = attempts+1 >= 3 || errors.As(err, &status) && status.Status >= 400 && status.Status < 500
		}
		if fallback {
			kind = pidriver.Urgent
			text = guidance
			err = nil
		}
	}
	if err == nil {
		err = d.Send(pidriver.Outgoing{Text: text, Kind: kind, Source: n.MessageID})
	}
	m.mu.Lock()
	if a.driver != d || a.def.Desired != "running" {
		return nil
	}
	if n.Kind == "interrupt" {
		if err == nil {
			delete(a.interruptFailures, n.MessageID)
		} else if fetchFailed && !fallback {
			if a.interruptFailures == nil {
				a.interruptFailures = make(map[string]int)
			}
			a.interruptFailures[n.MessageID] = attempts + 1
		}
	}
	if err != nil {
		a.pendingWakes = append(a.pendingWakes, n)
	}
	return nil
}
