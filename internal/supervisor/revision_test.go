package supervisor

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDefinitionRevisionPersists(t *testing.T) {
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	d.Version = 1
	d.Revision = 7
	if err := m.save(&managed{def: d}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.definitionPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	var stored AgentDefinition
	if err := json.Unmarshal(data, &stored); err != nil || stored.Revision != 7 {
		t.Fatalf("revision lost: %d, %v", stored.Revision, err)
	}
}
