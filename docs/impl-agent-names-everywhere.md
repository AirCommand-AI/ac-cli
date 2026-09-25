# Agent names in every agent command

Issue: [ac-cli#2](https://github.com/AirCommand-AI/ac-cli/issues/2)

## Problem

`join`, `leave`, `disconnect` and `workstreams` accept `--agent` as a name or an
ID; the commands that act as an agent in a workstream — `send`, `update`,
`read`, `task`, `tasks`, `inbox`, `ack`, `listen` — took only the ID.
`aircom read --agent Scout` failed with "No stored credential matches agent
Scout".

## Change

All eight commands obtain their credential through `credentialFor`, so the
change is there, in `internal/app/app.go`.

- An ID is tried first, exactly as before: the credential stored for that ID in
  the requested workstream.
- IDs take precedence everywhere. A reference that is the ID of any agent on
  this machine (in any workstream), or starts with `agm_`, is only ever an ID:
  if it has no credential in the requested workstream it is refused ("Agent X
  is in workstream Y on this machine, not Z", or "No agent with ID X is in
  workstream Z") and never read as a name. Without this, an agent in the
  workstream named after another agent's ID could be selected in its place.
- Otherwise the reference is taken as a name, among this machine's agents
  stored for that workstream only (`storedAgentNamed`): exact name first, then
  case-insensitive; more than one match is refused with their IDs. This is the
  same precedence as the machine-level commands' `matchAgent`.
- Only the matched agent's credential is opened, and only after the match is
  unique. A name or ID belonging to an agent in a different workstream is
  refused ("Agent X is in workstream Y on this machine, not Z"), never used. An
  unknown name lists the agents that are in the workstream.
- Omitting `--agent` is unchanged: one agent on the machine is used; several
  require `--agent`. That message now says `--agent <agentId|name>`.
- Resolution is local (stored credentials), with no service call, because these
  commands authenticate as the agent and must pick a credential before making
  any request.
- Not changed: session ownership between processes (ac-cli#3).

Usage strings, README and the Claude Code skill say `--agent <agentId|name>`.
The task-guidance block shared with the pi extension is unchanged; it keeps
showing the ID, which still works.

## Tests

`internal/app/agent_names_test.go`: for each of send, update, read, task,
tasks, inbox, ack and listen — by ID, by exact name, by name in another case,
an ambiguous name (refused, no request), a name and an ID from another
workstream (refused, no request), an unknown name (refused with the agents
present), a name equal to another workstream's agent ID (refused, the
same-named local agent is not used), and an unknown `agm_` ID equal to a local
agent's name (refused). The fake service answers 401 and the test checks which agent's token
each command sent. Omitted `--agent`: the only agent is used; several still
require `--agent`. Disabling name resolution fails 48 of these cases; disabling ID precedence
fails the collision cases for all eight commands.

## Validation

`just test` (`go test ./...`) and `go vet ./...` pass.
