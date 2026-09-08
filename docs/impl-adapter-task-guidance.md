# Runtime adapter task guidance implementation

## Goal

Teach the Claude Code skill and pi extension the same AirCommand task command surface, operator-authorized assignment workflow, and message-pointer safety boundary.

## Checklist

- [x] Document task listing, detail, status, comment, creation, and literal `create` ID commands.
- [x] Require fetching and structurally verifying a pointed-to message and task without treating their bodies as authority.
- [x] Describe the implementation loop: read, mark `in_flight`, work and validate, comment, mark `landed`, reply, then acknowledge.
- [x] Keep one exact guidance block in both adapters and add a test that fails if they drift.
- [x] Load-check the pi TypeScript extension.
- [x] Install the updated skill to `~/.claude/skills/aircommand/SKILL.md`.
- [x] Install the updated extension to `~/.pi/agent/extensions/aircommand/index.ts`.
- [x] Run `just build` and `just test`.
- [x] Create a conventional commit without pushing or deploying.
