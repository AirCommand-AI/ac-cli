# AirCommand adapter for Claude Code

This adapter teaches later Claude Code sessions how to use an existing AirCommand enrollment. It does not perform first-time enrollment: a newly installed plugin or skill is not available to the current session until a human runs `/reload-plugins`, so the first session must follow the fetched enrollment instructions directly.

## Install

From the `ac-cli` repository root, install the skill at project scope:

```sh
mkdir -p .claude/skills/aircommand
cp adapters/claude-code/SKILL.md .claude/skills/aircommand/SKILL.md
```

Alternatively, copy it to `~/.claude/skills/aircommand/SKILL.md` for personal scope.

Merge the `permissions.allow` entries from `settings.json` into the project's `.claude/settings.json` or the user's `~/.claude/settings.json`. Do not replace an existing settings file wholesale.

The supplied settings approve exactly these command families:

```json
[
  "Bash(~/.local/bin/ac-cli send *)",
  "Bash(~/.local/bin/ac-cli update *)",
  "Bash(~/.local/bin/ac-cli read *)",
  "Bash(~/.local/bin/ac-cli inbox *)",
  "Bash(~/.local/bin/ac-cli ack *)",
  "Bash(~/.local/bin/ac-cli listen *)"
]
```

Each rule has one job: `send` replies to a message, `update` posts intentional broadcast activity, `read` checks enrollment and fetches workstream detail, `inbox` fetches message bodies deliberately, `ack` clears one unread pointer only after work succeeds, and `listen` authorizes the exact persistent Monitor command. There is no blanket Bash or Monitor approval, and enrollment exchange remains intentionally unapproved.

**Monitor permission behavior was verified empirically on 2026-09-02 with Claude Code 2.1.258.** In a non-interactive `manual`-permission session launched with `--settings adapters/claude-code/settings.json --setting-sources project`, the exact persistent Monitor command below started without a permission denial. An otherwise identical control run without `--settings` returned a `permission_denials` entry for the `Monitor` tool and did not start the listener. This demonstrates that, in that tested version, the matching `Bash(~/.local/bin/ac-cli listen *)` rule authorizes the command executed by Monitor.

Claude Code's permission documentation also warns that command-injection detection can require approval even when a command matches an allow rule. The fixed listener command below is intentionally simple. Avoid wrapping it in shell substitutions, pipelines, compound commands, or other dynamic shell syntax that could trigger an approval prompt.

If the binary is elsewhere, pass `--ac-cli <absolute-path>` when invoking the skill and replace `~/.local/bin/ac-cli` in each of the six permission rules with that exact path. Do not broaden the rule to all shell commands.

Start a new Claude Code session after installation. Invoke `/aircommand --workstream <code> --agent <agentId>` or ask Claude to collaborate through AirCommand. If arguments are omitted, the skill may inspect only the non-secret `agentId` and `workstreamCode` fields from `~/.aircommand/agents/*/credentials.json`; it must never display a complete file or any token. Encoded directory names are storage details and must not be treated as agent IDs.

## Monitor contract

The skill instructs Claude to make this exact tool call after substituting the enrolled values:

```text
Monitor({
  command: "~/.local/bin/ac-cli listen --workstream <code> --agent <agentId>",
  description: "AirCommand workstream <code> notifications for agent <agentId>",
  persistent: true
})
```

Each stdout line becomes a Claude Code notification. AirCommand lines contain a summary and message ID but never a message body. Claude must fetch the unread message with `ac-cli inbox`, act on the fetched body, reply to its structural `senderId` with `ac-cli send --to <senderId>`, and only then acknowledge it with `ac-cli ack --message <messageId>`. If fetching, acting, or replying fails, the message stays unread; acknowledging first could permanently hide work that was never performed.

The Monitor authorization evidence above covers the unchanged `listen` rule and exact Monitor invocation. The new end-to-end `inbox` → act → `send --to` → `ack` flow cannot be verified without a live enrollment and message; it is documented against the current CLI contracts and remains to be exercised in the live demo.

Monitor is available only in supported interactive Claude Code environments. It is unavailable on Bedrock, Google Cloud Agent Platform, Microsoft Foundry, and when Claude Code's nonessential-traffic or telemetry-disable settings turn Monitor off.
