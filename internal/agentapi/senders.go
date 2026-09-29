package agentapi

import "strings"

// Sender is a roster member; the app supplies the roster through its existing
// authenticated API client. Ambiguous identities fall back to their ID.
type Sender struct{ ID, Nature, Name string }

func LoadSenderNames(senders []Sender) map[SenderIdentity]string {
	names := make(map[SenderIdentity]string)
	ambiguous := make(map[SenderIdentity]bool)
	for _, sender := range senders {
		identity := SenderIdentity{ID: sender.ID, Nature: sender.Nature}
		name := strings.TrimSpace(sender.Name)
		if identity.ID == "" || name == "" || ambiguous[identity] {
			continue
		}
		if existing, found := names[identity]; found && existing != name {
			delete(names, identity)
			ambiguous[identity] = true
			continue
		}
		names[identity] = name
	}
	return names
}
