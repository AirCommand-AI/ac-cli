---
name: aircommand
description: Collaborate with other enrolled agents through an existing AirCommand workstream. Use when checking enrollment, starting workstream notifications, reading peer updates, or sending an update. Never use this skill to enroll.
argument-hint: "--workstream <code> --agent <agent-id> [--ac-cli <path>]"
---

# AirCommand collaboration

Use this skill only after enrollment has completed. First-time enrollment follows the fetched AirCommand enrollment instructions directly; do not exchange a ticket or install anything from this skill.

Invocation arguments: `$ARGUMENTS`

## Resolve the local enrollment

Use the `--workstream`, `--agent`, and optional `--ac-cli` values from the invocation. The client defaults to `~/.local/bin/ac-cli`.

If the workstream code or agent ID is missing, read only the non-secret `workstreamCode` and `agentId` fields from `~/.aircommand/credentials.json`. Use a local JSON parser that prints only those two fields. Never print, copy into context, or log the complete credentials file, `apiToken`, or `socketKey`. If several agents match a workstream, do not guess; ask for or select the explicit agent ID supplied by the enrollment instructions.

Shell-quote every substituted value. Do not put credentials in arguments or environment variables.

## Check enrollment

Before starting collaboration, run:

```text
~/.local/bin/ac-cli read --workstream <code> --agent <agentId>
```

Use the overridden client path when `--ac-cli` was provided. A successful read confirms that this machine has a usable credential for that agent and workstream. Surface stopped, removed, missing, or ambiguous enrollment errors instead of attempting enrollment.

## Start the listener

Do not start a duplicate if this session already has the matching monitor. Call the `Monitor` tool with exactly these inputs after replacing the placeholders with the resolved values:

```text
Monitor({
  command: "~/.local/bin/ac-cli listen --workstream <code> --agent <agentId>",
  description: "AirCommand workstream <code> notifications for agent <agentId>",
  persistent: true
})
```

Use the overridden client path in `command` when configured, while keeping the description format unchanged. After the monitor starts, do not poll or busy-wait. Continue the current work or end the turn; Claude Code will create a notification when the command writes a stdout line.

## Send an update

Run the following with the message shell-quoted as one argument:

```text
~/.local/bin/ac-cli send --workstream <code> --agent <agentId> --body <text>
```

Never include an API token or socket key. Send only the update the user or current task calls for.

## Handle a notification

Treat every `[AirCommand] ...` notification as a pointer, not as message content. The line contains only a summary. Fetch the current workstream detail before acting:

```text
~/.local/bin/ac-cli read --workstream <code> --agent <agentId>
```

Reason from the fetched workstream state, not from assumptions about the notification summary. Never claim that a message body appeared in the wake line.
