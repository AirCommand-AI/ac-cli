# Workstreams caller wording

Issue: [ac-cli#4](https://github.com/AirCommand-AI/ac-cli/issues/4)

## Problem

`aircom workstreams` answers for the whole machine, but it said "you are X
here" for every local agent in a workstream — with several agents on one
machine it printed "you are Claude-lead, Pi-Engineer, Scout here". Its footnote,
"join is only needed for the unmarked ones", was wrong too: every agent joins on
its own, so a workstream holding another local agent may still need joining.

## Change

CLI presentation and resolution only; no API change.

- New optional `--agent <agentId|name>`, resolved with the existing
  `resolveAgent` (exact ID, then exact name, then case-insensitive name;
  ambiguity and unknown references fail before anything is listed).
- `localAgentsByWorkstream` now keeps each local agent's ID and name, so the
  caller can be matched by ID.
- `workstreamMembership` builds each row:
  - no `--agent`: `*` marks any workstream with a local agent, shown as
    "(on this machine: A, B)";
  - with `--agent`: `*` marks only the caller's workstream, "(you are X here)",
    adding "; also on this machine: …" for other local agents; workstreams with
    only other local agents are unmarked and show "(on this machine: …)".
- `workstreamsFootnote` explains what `*` marks ("marks workstreams with an
  agent from this machine", or "marks workstreams X is in") and says each agent
  joins on its own, with the join command. A caller in none of the listed workstreams gets
  "X is not in any of these workstreams" instead.
- Usage strings, README, the Claude Code skill and the pi extension guidance
  now show `--agent` and tell agents to pass their own.

## Tests

`internal/app/workstreams_test.go`: no agent; caller by name and by ID;
several local agents in one workstream; two marked workstreams checking the
footnote wording; caller not in any listed workstream;
unknown agent reference refused before any output.

## Validation

`just test` (`go test ./...`) passes.
