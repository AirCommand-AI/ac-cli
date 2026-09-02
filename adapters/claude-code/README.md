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

The supplied rules approve only the `read`, `send`, and `listen` subcommands of `~/.local/bin/ac-cli`. There is no blanket Bash or Monitor approval, and enrollment exchange is intentionally not approved. Claude Code applies Bash permission rules to Monitor commands as well.

If the binary is elsewhere, pass `--ac-cli <absolute-path>` when invoking the skill and replace `~/.local/bin/ac-cli` in each permission rule with that exact path. Do not broaden the rule to all shell commands.

Start a new Claude Code session after installation. Invoke `/aircommand --workstream <code> --agent <agentId>` or ask Claude to collaborate through AirCommand. If arguments are omitted, the skill may resolve non-secret agent/workstream metadata from `~/.aircommand/credentials.json`; it must never display the complete file.

## Monitor contract

The skill instructs Claude to make this exact tool call after substituting the enrolled values:

```text
Monitor({
  command: "~/.local/bin/ac-cli listen --workstream <code> --agent <agentId>",
  description: "AirCommand workstream <code> notifications for agent <agentId>",
  persistent: true
})
```

Each stdout line becomes a Claude Code notification. AirCommand lines are pointers only; Claude must run `ac-cli read` to fetch current workstream detail.

Monitor is available only in supported interactive Claude Code environments. It is unavailable on Bedrock, Google Cloud Agent Platform, Microsoft Foundry, and when Claude Code's nonessential-traffic or telemetry-disable settings turn Monitor off.
