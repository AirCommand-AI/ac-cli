# Re-arm an expired Claude Code listener

Issue: [ac-cli#1](https://github.com/AirCommand-AI/ac-cli/issues/1)

## Problem

Claude Code's `Monitor` tool stops every watch after at most 30 minutes, whatever
timeout is requested, and stops the `aircom listen` process with it — although
the listener is healthy. Claude-lead asked for 60 minutes and got 30. Until the
agent starts it again, the agent is in the workstream but hears nothing, and no
one else can tell.

## Change

Guidance only; no CLI behaviour change.

- `adapters/claude-code/SKILL.md` gains "When the listener's watch expires":
  - recognise the notice `[Monitor expired after 30m … Re-arm it if you still
    need the watch.]`;
  - re-arm straight away with exactly one `Monitor` call of
    `aircom listen --workstream <code> --agent <agentId>`, persistent — the
    `listen` form even if the expired monitor ran `join --listen`; the
    `join --listen` form only if the agent was still waiting to be placed;
  - re-arm only after an expiry notice, never alongside a running listener;
  - a listener that exited on its own (not registered, not in the workstream or
    removed, permission denied, agent running in another session) is not an
    expiry: report it and stop;
  - after re-arming, run `aircom inbox` once for messages sent in the gap, and
    acknowledge only after acting.
- `adapters/claude-code/README.md` summarises the same under "Monitor contract".

## Limitation

Re-arming depends on Claude Code surfacing the expiry notice. On 2026-09-24/25
the notice arrived as a notification that woke an idle session, but that is
Claude Code's behaviour, not something AirCommand controls. A session that is
closed, or on a sleeping machine, re-arms nothing until it resumes. Showing
whether each agent is actually listening is tracked separately
(ac-dashboard#7).

## Tests

`internal/app/adapter_guidance_test.go`:
`TestClaudeCodeSkillReArmsAnExpiredListener` checks the section names the
notice, gives the exact `listen` Monitor call, limits to one listener, excludes
real failures, catches up on the inbox without early acknowledgement, and
states the limitation.

## Validation

`just test` (`go test ./...`) passes.
