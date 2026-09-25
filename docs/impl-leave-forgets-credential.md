# Leaving forgets the workstream credential and cursor

Issue: [ac-cli#8](https://github.com/AirCommand-AI/ac-cli/issues/8)

## Problem

`aircom leave` ended the agent's membership on the server but left its
workstream credential on disk. `aircom workstreams` builds its "on this machine"
list from stored credentials, so an agent kept appearing in the workstream it
had left — Scout and Scout-SCIM still showed in 583 and 345 after leaving.

## Change

- `credentials.Store.Delete(agentID)` removes only that agent's
  `credentials.json`. The rest of the agent's directory stays: its lock,
  listener cursor and spool may belong to a listener still running in another
  process. Deleting an absent credential is not an error.
- `leave`, after the server confirms (2xx), calls `forgetWorkstreamCredential`.
  If the server refuses, nothing local changes, so a still-valid credential is
  never discarded.
- If the local delete fails after the server succeeded, `leave` exits non-zero
  and says so: the agent has left, the credential could not be removed, delete
  the named file or run `aircom leave --agent <name>` again. No token or key is
  printed.
- The server treats a repeat leave as success, so running `leave` again also
  clears a credential left by an earlier leave (how existing stale entries,
  such as Scout's, are cleaned up).
- `disconnect` leaves the workstream first, so it forgets the credential the
  same way.
- A listener still running for the agent keeps its in-memory credential; the
  server has revoked it, so the listener stops on its next poll.

Rejoining is unchanged: `join` saves a fresh credential for the new workstream.

### Listener cursor bound to its workstream

The listener's cursor (`state.json`, `listenstore`) is a position in one
workstream's notification feed but was stored per agent only, so after leave
and join elsewhere the old position was sent as `since` to the new workstream —
able to skip its messages or page wrongly.

- Workstream codes repeat across organizations, so the cursor is tagged with
  the workstream's key: organization ID and code. `join` now stores the
  organization it joined in on the credential (`organizationId`), and
  `Credential.WorkstreamKey()` returns `<organizationId>/<code>`. No extra
  server call: join already knows the organization, since it sends it as the
  request's organization header.
- `cursorState` records that key; `SaveCursor(agentID, key, cursor)` writes it
  and `LoadCursor(agentID, key)` reports a cursor saved under any other key —
  or with no key — as absent, so the listener takes a fresh baseline.
- Credentials written before this change (or by the setup-link exchange) have
  no organization; their key is `/<code>`. That is stable across restarts, so
  same-workstream restarts still resume. Moving always goes through `join`,
  which writes a credential with the organization, so a move always changes the
  key and starts fresh.
- A cursor saved before this change has no key and gets one fresh baseline,
  after which the key is recorded.
- A fresh baseline is silent: it records the current position and announces
  nothing already in the feed. Messages that arrived before it are not woken
  for, but stay unread on the server and show in `aircom inbox` (the Claude
  Code re-arm guidance runs it once).
- An old listener still running during leave can only write a cursor tagged
  with the old workstream's key, which a listener elsewhere ignores.
- Same-workstream restarts resume from the stored cursor. The spool and lock
  are untouched.

## Tests

`internal/app/leave_test.go`, against a stateful fake of the service:

- successful leave removes only that agent's credential; listing without
  `--agent`, as the agent that left, and as the agent that stayed;
- failed leave keeps the credential and the listing;
- local delete failure is reported with the path and recovery, leaks no
  secret, and a repeat leave then clears it;
- the agent's lock file and directory survive leave;
- rejoin after leave lists the agent in its new workstream;
- disconnect forgets the credential and keeps the other agent's.
- listen in the same workstream resumes from its cursor; after leave and join
  elsewhere, the first poll carries no `since` and the new baseline is stored
  for the new workstream (`leave_test.go`);
- moving from organization A's 610 to organization B's 610: the restart in A
  resumes, the first poll in B carries no `since`, and join stores A's
  organization on the credential (`leave_test.go`);
- `listenstore`: same-workstream resume, different workstream fresh, a stale
  listener's late write ignored, pre-change cursor fresh.

Three of the leave tests fail against the previous `leave`/`disconnect`; the
cursor tests fail when `LoadCursor` ignores the workstream, and the
cross-organization test fails when the key leaves out the organization.

## Validation

`just test` (`go test ./...`) and `go vet ./...` pass.
