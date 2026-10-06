package app

import (
	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
	"strings"
	"testing"
)

// Workstream codes are unique only within an organization. These tests put
// Lead in Acme's 610 and Engineer in Beta's 610 and check that listing and
// joining never confuse the two.

func crossOrgFixture(t *testing.T) (*leaveServer, *App, func(args ...string) (int, string, string)) {
	t.Helper()
	fake, client, stdout, stderr := leaveFixture(t)
	fake.mu.Lock()
	fake.workstreams[leadID], fake.orgs[leadID] = "610", acmeID
	fake.workstreams[engineerID], fake.orgs[engineerID] = "610", betaID
	fake.mu.Unlock()
	// The service says Lead is already in Acme's 610, so the locally stored
	// bearer must describe that same workstream for join's liveness check.
	lead, err := client.Store.FindByAgent("583", leadID)
	if err != nil {
		t.Fatal(err)
	}
	lead.WorkstreamCode, lead.OrganizationID = "610", acmeID
	if err := client.Store.Save(lead); err != nil {
		t.Fatal(err)
	}
	return fake, client, func(args ...string) (int, string, string) {
		return run(t, client, stdout, stderr, args...)
	}
}

func TestWorkstreamsShowOnlyAgentsInThatOrganization(t *testing.T) {
	_, _, exec := crossOrgFixture(t)

	cases := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"Acme without --agent", []string{"workstreams", "--org", "Acme"},
			[]string{"* 610      Open    Fixes  (on this machine: Lead)\n"}, []string{"Engineer"}},
		{"Beta without --agent", []string{"workstreams", "--org", "Beta"},
			[]string{"* 610      Open    Fixes  (on this machine: Engineer)\n"}, []string{"Lead"}},
		{"Acme as Lead", []string{"workstreams", "--org", "Acme", "--agent", "Lead"},
			[]string{"* 610      Open    Fixes  (you are Lead here)\n"}, []string{"Engineer"}},
		{"Beta as Lead: Lead's 610 is Acme's, not Beta's", []string{"workstreams", "--org", "Beta", "--agent", "Lead"},
			[]string{"  610      Open    Fixes  (on this machine: Engineer)\n", "Lead is not in any of these workstreams"}, []string{"you are Lead"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errText := exec(tc.args...)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, errText)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("contains %q:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestWorkstreamsDoNotClaimAnAgentWithNoOrganization(t *testing.T) {
	fake, _, exec := crossOrgFixture(t)
	fake.mu.Lock()
	fake.orgs[leadID] = "" // the service has a code but no organization for Lead
	fake.mu.Unlock()

	for _, org := range []string{"Acme", "Beta"} {
		code, out, errText := exec("workstreams", "--org", org)
		if code != 0 {
			t.Fatalf("%s: exit code = %d, stderr = %q", org, code, errText)
		}
		if strings.Contains(out, "Lead") {
			t.Fatalf("%s listing claimed Lead with no organization:\n%s", org, out)
		}
	}
}

func TestJoinComparesOrganizationAndCode(t *testing.T) {
	cases := []struct {
		name     string
		org      string
		wantExit int
		wantOut  string
		wantErr  string
	}{
		{name: "same organization and code resumes", org: "Acme", wantOut: "Agent ID: " + leadID},
		{name: "same code in another organization is refused", org: "Beta", wantExit: 1,
			wantErr: "Lead is already in workstream 610 in another organization. Take it out first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, exec := crossOrgFixture(t)
			code, out, errText := exec("join", "--agent", "Lead", "--org", tc.org, "--workstream", "610")
			if (code != 0) != (tc.wantExit != 0) {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, out, errText)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("stdout missing %q: %q", tc.wantOut, out)
			}
			if tc.wantErr != "" && !strings.Contains(errText, tc.wantErr) {
				t.Errorf("stderr missing %q: %q", tc.wantErr, errText)
			}
			fake.mu.Lock()
			org := fake.orgs[leadID]
			fake.mu.Unlock()
			if org != acmeID {
				t.Fatalf("Lead's organization on the service is now %q; neither case may move it", org)
			}
		})
	}
}

func TestLeaveThenJoinTheSameCodeInAnotherOrganization(t *testing.T) {
	fake, _, exec := crossOrgFixture(t)

	for _, step := range [][]string{
		{"leave", "--agent", "Lead"},
		{"join", "--agent", "Lead", "--org", "Beta", "--workstream", "610"},
	} {
		if code, _, errText := exec(step...); code != 0 {
			t.Fatalf("%v exit code = %d, stderr = %q", step, code, errText)
		}
	}
	fake.mu.Lock()
	org := fake.orgs[leadID]
	fake.mu.Unlock()
	if org != betaID {
		t.Fatalf("Lead's organization on the service = %q; want Beta's", org)
	}
	_, beta, _ := exec("workstreams", "--org", "Beta")
	if !strings.Contains(beta, "(on this machine: Engineer, Lead)") {
		t.Fatalf("Beta listing after the move:\n%s", beta)
	}
	_, acme, _ := exec("workstreams", "--org", "Acme")
	if strings.Contains(acme, "Lead") {
		t.Fatalf("Acme listing still claims Lead after it moved:\n%s", acme)
	}
}

// TestLeaveFromAnAttachedSessionDetachesIt covers a pi that leaves: the daemon
// must stop treating the session as that agent, or the add-on keeps reporting
// for the workstream it left.
func TestLeaveFromAnAttachedSessionDetachesIt(t *testing.T) {
	_, client, exec := crossOrgFixture(t)
	fake := &fakeSessionControl{}
	client.SessionClient = fake
	client.ProcessSnapshot = func(int) (int, string, string, error) { return 1, "pi", "start", nil }
	if code, _, errText := exec("leave", "--agent", "Lead"); code != 0 {
		t.Fatalf("leave exit code = %d, stderr = %q", code, errText)
	}
	if len(fake.detaches) != 1 || !strings.HasSuffix(fake.detaches[0], "|left the workstream") {
		t.Fatalf("detaches = %v, want one 'left the workstream'", fake.detaches)
	}
}

func TestAgentNamesNeverStartWithADash(t *testing.T) {
	for name, want := range map[string]bool{"--help": false, "-x": false, "eng-1": true, "_svc": true, "Lead": true} {
		if got := daemonclient.ValidAgentName(name); got != want {
			t.Errorf("ValidAgentName(%q) = %v, want %v", name, got, want)
		}
	}
}
