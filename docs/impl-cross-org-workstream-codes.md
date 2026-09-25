# Same workstream code in different organizations

Issue: [ac-cli#9](https://github.com/AirCommand-AI/ac-cli/issues/9)

## Problem

Workstream codes are unique only within an organization. Two places compared
the code alone:

- `aircom workstreams --org B` built its "on this machine" annotations from
  stored credentials, which carry only a code, so an agent in A's 610 showed as
  being in B's 610.
- `aircom join --org B --workstream 610` checked the agent's code before its
  organization, so an agent already in A's 610 was reported as resumed in B's.

## Change

- The listing now takes membership from the service's record of this machine's
  agents (`GET /v1/agents`, which returns each agent's `organizationId` and
  `workstreamCode`), not from local credentials. `agentsByWorkstream` keeps only
  agents whose organization is the one being listed. An agent the service shows
  with a code but no organization is never claimed for any organization.
- `--agent` is matched against that same list (`matchAgent`, split out of
  `resolveAgent`), so a listing makes one `/v1/agents` call — the same one
  `--agent` already made.
- `join` treats "already there" as the same code **and** the same organization
  as the service records, and never when the recorded organization is empty.
  The same code in another organization is refused with "already in workstream
  610 in another organization. Take it out first: aircom leave --agent …".
  A dashboard pickup of an agent already joined carries the agent's own
  organization into that check.
- `backfillAgentNames` and `rosterNameFor` are removed: they named stored agents
  for the listing, which now gets names from the service.

Legacy credentials without an organization no longer matter to listing or join:
both use the service's record, which always has the organization for a joined
agent. The listener's cursor tag (ac-cli#8) still uses the credential.

## Tests

`internal/app/cross_org_test.go`, Lead in Acme/610 and Engineer in Beta/610:

- listing Acme and Beta, each without `--agent` and as Lead: each shows only its
  own agent, and Lead is not "you" in Beta;
- an agent with a code but no organization is claimed by neither listing;
- join Acme/610 resumes; join Beta/610 while in Acme is refused and leaves the
  service record unchanged;
- leave, then join Beta/610: the service records Beta, and only Beta's listing
  shows Lead.

Existing listing and leave tests now supply `organizationId` in their fake
service, as the real one does. The new tests fail when the organization check
is removed from the listing and from join.

## Validation

`just test` (`go test ./...`) and `go vet ./...` pass.
