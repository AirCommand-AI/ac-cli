package supervisor

import (
	"context"
	"fmt"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

// WaitReady ensures socket catch-up starts after all persisted agent
// definitions have been re-adopted at daemon boot.
func (m *Manager) WaitReady(ctx context.Context) error {
	select {
	case <-m.booted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wake writes a pushed pointer without touching the cursor. The manager lock
// serializes it with polling and the persisted 500-ID dedupe set survives a
// daemon restart. Ignore pushes for agents not running on this machine.
func (m *Manager) Wake(ctx context.Context, agentID string, n agentapi.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.Type != "message.received" || n.MessageID == "" || n.SenderID == "" {
		return fmt.Errorf("invalid wake pointer")
	}
	for _, a := range m.agents {
		if a.def.AgentID != agentID {
			continue
		}
		if a.def.Desired != "running" || a.def.State == "stopped-by-dashboard" || a.def.State == "crashed" {
			return nil
		}
		for _, id := range a.delivered {
			if id == n.MessageID {
				return nil
			}
		}
		item, err := m.Poll.Spool(ctx, a.def, n)
		if err != nil {
			return err
		}
		if err = listenstore.NewStore(m.Home).AppendNotification(agentID, item); err != nil {
			return err
		}
		a.delivered = append(a.delivered, n.MessageID)
		if len(a.delivered) > 500 {
			a.delivered = a.delivered[len(a.delivered)-500:]
		}
		if err := atomicJSON(m.deliveredPath(agentID), a.delivered); err != nil {
			return err
		}
		return m.deliver(ctx, a, n)
	}
	return nil
}

// CatchUp polls every running agent immediately after a socket connection.
// Normal periodic polling remains active for heartbeat and missed pushes.
func (m *Manager) CatchUp(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.Desired != "running" || a.def.State != "running" {
			continue
		}
		if err := m.poll(ctx, a); err != nil {
			return err
		}
	}
	return nil
}
