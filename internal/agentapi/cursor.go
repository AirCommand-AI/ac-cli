package agentapi

// CursorStore is implemented by listenstore.Store. The key is the agent's
// credential WorkstreamKey, not the display code.
type CursorStore interface {
	LoadCursor(agentID, workstreamKey string) (string, bool, error)
	SaveCursor(agentID, workstreamKey, cursor string) error
}

func LoadCursor(store CursorStore, agentID, workstreamKey string) (string, bool, error) {
	return store.LoadCursor(agentID, workstreamKey)
}
func SaveCursor(store CursorStore, agentID, workstreamKey, cursor string) error {
	return store.SaveCursor(agentID, workstreamKey, cursor)
}
