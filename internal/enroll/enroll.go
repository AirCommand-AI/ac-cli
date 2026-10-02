// Package enroll contains machine-authenticated agent registration, join and
// organization selection shared by the CLI and daemon. A caller supplies its
// authenticated transport and persists the returned agent credential only on
// a successful response.
package enroll

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type Response struct {
	Status int
	Body   []byte
}
type Request func(method, path, token string, payload []byte) (Response, error)

type Organization struct {
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
}

type Joined struct {
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	WorkstreamCode string `json:"workstreamCode"`
	SocketAddress  string `json:"socketAddress"`
}

func Register(request Request, machineToken, name string) (Response, error) {
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{name})
	if err != nil {
		return Response{}, err
	}
	return request(http.MethodPost, "/v1/agents", machineToken, body)
}

// Join sends a newly generated bearer even for an already-bound agent. This
// is needed after its previous session is revoked by dashboard Stop; the
// server accepts the rejoin when there is no active session. The caller must
// choose the organization's request scope and store the new bearer on success.
func Join(request Request, machineToken, agentID, workstream, newToken, idempotencyID string) (Response, error) {
	body, err := json.Marshal(struct {
		APIToken      string `json:"apiToken"`
		IdempotencyID string `json:"idempotencyId"`
	}{newToken, idempotencyID})
	if err != nil {
		return Response{}, err
	}
	return request(http.MethodPost, "/v1/agents/"+agentID+"/workstreams/"+workstream, machineToken, body)
}

// ResolveOrganization accepts an exact ID or name, then a case-insensitive
// name. Ambiguous names fail closed rather than selecting an arbitrary org.
func ResolveOrganization(list []Organization, reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", fmt.Errorf("Name the organization with --org. Run aircom orgs to see them.")
	}
	for _, org := range list {
		if org.OrganizationID == reference {
			return org.OrganizationID, nil
		}
	}
	var exact, folded []Organization
	for _, org := range list {
		switch {
		case org.Name == reference:
			exact = append(exact, org)
		case strings.EqualFold(strings.TrimSpace(org.Name), reference):
			folded = append(folded, org)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = folded
	}
	switch len(matches) {
	case 1:
		return matches[0].OrganizationID, nil
	case 0:
		names := make([]string, 0, len(list))
		for _, org := range list {
			names = append(names, org.Name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "", fmt.Errorf("This machine can reach no organizations.")
		}
		return "", fmt.Errorf("No organization called %q. This machine can reach: %s", reference, strings.Join(names, ", "))
	default:
		ids := make([]string, 0, len(matches))
		for _, org := range matches {
			ids = append(ids, org.OrganizationID)
		}
		sort.Strings(ids)
		return "", fmt.Errorf("More than one organization is called %q. Use its identifier: %s", reference, strings.Join(ids, ", "))
	}
}
