package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func leadProfile() *AgentProfile {
	return &AgentProfile{Role: "eng-lead", RoleLabel: "Eng Lead", RolePrompt: "## Your role: Eng Lead\nKeep everyone busy.", RolePromptRev: 3, Model: "gpt-6-sol", Effort: "high", OrganizationName: "Builder Magic"}
}

func TestBriefText(t *testing.T) {
	base := AgentDefinition{Name: "lead", Organization: "org_1", Workstream: "766", WorkFolder: "/home/agent/work/lead"}
	withRole := base
	withRole.Profile = leadProfile()
	noPrompt := base
	noPrompt.Profile = &AgentProfile{OrganizationName: "Builder Magic"}
	cases := []struct {
		name string
		def  AgentDefinition
		want []string
		not  []string
	}{
		{"no profile", base, []string{"You are lead in workstream 766 (workspace org_1). Work only in /home/agent/work/lead.\n", "aircom inbox"}, []string{"Your role"}},
		{"role", withRole, []string{"You are lead, the Eng Lead in workstream 766 (workspace Builder Magic).", "\n\n## Your role: Eng Lead\nKeep everyone busy.\n"}, nil},
		{"workspace name only", noPrompt, []string{"(workspace Builder Magic)"}, []string{"the "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := briefText(tc.def)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("brief missing %q:\n%s", w, got)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(got, w) {
					t.Fatalf("brief has %q:\n%s", w, got)
				}
			}
		})
	}
}

func TestStartWritesBriefAndPassesModelAndEffort(t *testing.T) {
	ctx := context.Background()
	m, tm, _, _ := setup(t)
	d := definition(m.Home)
	d.Profile = leadProfile()
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	args := tm.launches[len(tm.launches)-1]
	for _, pair := range [][2]string{{"--model", "openai-codex/gpt-6-sol"}, {"--thinking", "high"}} {
		i := slices.Index(args, pair[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
			t.Fatalf("args %v missing %v", args, pair)
		}
	}
	brief, err := os.ReadFile(m.briefPath(d.AgentID))
	if err != nil || !strings.Contains(string(brief), "Keep everyone busy.") {
		t.Fatalf("brief: %q %v", brief, err)
	}
	info, err := os.Stat(m.briefPath(d.AgentID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("brief mode: %v %v", info, err)
	}
}

func TestStartWithoutProfileHasNoModelFlags(t *testing.T) {
	m, tm, _, _ := setup(t)
	if err := m.Start(context.Background(), definition(m.Home)); err != nil {
		t.Fatal(err)
	}
	args := tm.launches[len(tm.launches)-1]
	if slices.Contains(args, "--model") || slices.Contains(args, "--thinking") {
		t.Fatalf("unexpected flags: %v", args)
	}
	if _, err := os.Stat(m.briefPath("agm_1")); err != nil {
		t.Fatalf("brief not written: %v", err)
	}
}

func TestHeadlessLaunchPassesModelAndEffort(t *testing.T) {
	m, _, _, _ := setup(t)
	fake := pidriver.NewFake()
	m.NewDriver = func(io.Writer) pidriver.Driver { return fake }
	d := definition(m.Home)
	d.Mode = "headless"
	d.Profile = leadProfile()
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(fake.Launches) != 1 || !slices.Contains(fake.Launches[0].Args, "openai-codex/gpt-6-sol") || !slices.Contains(fake.Launches[0].Args, "--thinking") {
		t.Fatalf("launches: %+v", fake.Launches)
	}
}

func TestUpdateProfileSavesWithoutRestart(t *testing.T) {
	ctx := context.Background()
	m, tm, _, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	launches := len(tm.launches)
	changed, err := m.UpdateProfile(d.Name, leadProfile())
	if err != nil || !changed {
		t.Fatalf("update: %v %v", changed, err)
	}
	if again, err := m.UpdateProfile(d.Name, leadProfile()); err != nil || again {
		t.Fatalf("same profile reported a change: %v %v", again, err)
	}
	if len(tm.launches) != launches || m.agents[d.Name].def.State != "running" {
		t.Fatalf("profile update restarted the agent: %d launches, state %s", len(tm.launches), m.agents[d.Name].def.State)
	}
	// A start without a profile (aircom agent start) keeps the saved one.
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	if p := m.agents[d.Name].def.Profile; p == nil || p.Role != "eng-lead" {
		t.Fatalf("start dropped the profile: %+v", p)
	}
	data, err := os.ReadFile(m.definitionPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	var saved AgentDefinition
	if err := json.Unmarshal(data, &saved); err != nil || saved.Profile == nil || saved.Profile.RolePromptRev != 3 {
		t.Fatalf("profile not saved: %+v %v", saved.Profile, err)
	}
}

func TestProfileValidation(t *testing.T) {
	m, _, _, _ := setup(t)
	cases := []struct {
		name string
		p    *AgentProfile
	}{
		{"prompt too long", &AgentProfile{Role: "engineer", RolePrompt: strings.Repeat("a", MaxRolePromptBytes+1)}},
		{"model with space", &AgentProfile{Model: "gpt 6"}},
		{"effort with flag", &AgentProfile{Effort: "--high"}},
		{"model with flag", &AgentProfile{Model: "-m"}},
		{"label with newline", &AgentProfile{RoleLabel: "Eng\nLead"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := definition(m.Home)
			d.Profile = tc.p
			if err := m.Start(context.Background(), d); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
	d := definition(m.Home)
	d.Harness = "claude-code"
	if err := m.Start(context.Background(), d); err == nil {
		t.Fatal("non-pi harness accepted")
	}
}
