# Runtime adapters

Implement T7/T8 as thin Claude Code and pi.dev adapters over the existing `ac-cli` output contracts.

- [x] Add the Claude Code AirCommand skill without enrollment behavior
- [x] Document the exact persistent Monitor invocation
- [x] Add a narrowly scoped `ac-cli` permission rule
- [x] Add a pi.dev extension that tails the spool from end-of-file
- [x] Resolve pi configuration from flags or non-secret credential metadata
- [x] Inject pointer notifications with pi's supported wake API
- [x] Add standalone installation and usage documentation for both adapters
- [x] Validate formats, pi extension loading, and spool-to-message behavior
- [x] Empirically verify whether Bash allow rules authorize Monitor commands
- [x] Document the verification result and command-injection approval caveat

Pi supports direct asynchronous `sendMessage(..., { triggerTurn: true })`; no before-turn lifecycle hook is needed. The adapter uses `followUp` while busy rather than community wake extensions' mid-turn `steer` behavior.

T7 was subsequently validated live on workstream 694: Monitor survived a connection reset, emitted failure/recovery lines, and woke on a posted message. A Claude Code 2.1.258 manual-permission run with this adapter's settings started the exact Monitor command; the control run without the settings returned a `Monitor` permission denial.

W12a restructures the pi.dev adapter for runtime connection and W6 storage.

- [x] Add an agent-callable runtime connect tool
- [x] Add human runtime connect and disconnect commands
- [x] Make sessions without an explicit AirCommand selection no-op quietly
- [x] Preserve startup flags as an explicit override
- [x] Tail the W6 per-agent spool with matching agent ID sanitisation
- [x] Preserve the verified follow-up wake mechanism exactly
- [x] Validate runtime lifecycle and load the extension under real pi
- [x] Update pi adapter documentation

Validated with pi 0.84.4 in RPC mode: flagless startup was silent against both an empty home and this machine's legacy storage, runtime connect/disconnect created only the per-agent spool, startup flags still connected, and a probe confirmed `aircommand_connect` was registered. A direct lifecycle harness also verified pointer delivery, disconnect/reconnect, crafted-ID confinement, no replay while disconnected, and the unchanged `followUp`/`triggerTurn` options.
