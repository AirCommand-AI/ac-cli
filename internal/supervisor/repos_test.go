package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestAddRepoSavesWithoutRestart(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		changed bool
		want    []string
	}{
		{"new repo", "org/second", true, []string{"org/repo", "org/second"}},
		{"already saved", "ORG/Repo", false, []string{"org/repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, tm, _, _ := setup(t)
			if err := m.Start(context.Background(), definition(m.Home)); err != nil {
				t.Fatal(err)
			}
			launches := len(tm.launches)
			changed, err := m.AddRepo("eng-1", tc.repo)
			if err != nil || changed != tc.changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if len(tm.launches) != launches {
				t.Fatalf("agent relaunched: %d -> %d", launches, len(tm.launches))
			}
			if got := m.Definitions()[0].Repos; !slices.Equal(got, tc.want) {
				t.Fatalf("repos=%v want %v", got, tc.want)
			}
			data, err := os.ReadFile(m.definitionPath("agm_1"))
			if err != nil {
				t.Fatal(err)
			}
			var saved AgentDefinition
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(saved.Repos, tc.want) {
				t.Fatalf("saved repos=%v want %v", saved.Repos, tc.want)
			}
		})
	}
	m, _, _, _ := setup(t)
	if _, err := m.AddRepo("missing", "org/x"); !os.IsNotExist(err) {
		t.Fatalf("missing agent err=%v", err)
	}
}
