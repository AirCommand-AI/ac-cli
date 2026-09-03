# Core agent commands

Implement the initial `ac-cli` repository with secure enrollment exchange and authenticated workstream send/read commands.

- [x] Scaffold the Go 1.25.5 repository and `just` tasks
- [x] Implement credential generation and secure credential persistence
- [x] Implement `exchange`, including stdin-only tickets and idempotent HTTP retries
- [x] Implement authenticated `send` and `read`
- [x] Map API failures to safe, actionable messages without leaking response bodies
- [x] Add unit and `httptest` integration coverage
- [x] Run formatting, tests, and build validation
- [x] Commit the implementation in the new repository

Follow-up: support multiple local agents in one workstream and enforce the verified exchange response contract.

- [x] Add `--agent <agentId>` selection to `send` and `read`
- [x] List matching agent IDs when workstream-only selection is ambiguous
- [x] Print the exchanged agent ID prominently with selector guidance
- [x] Replace speculative exchange decoding with the exact flat response schema
- [x] Keep non-message command retries limited to transport failures, never HTTP status responses
- [x] Add multi-agent selection and strict response-contract tests
- [x] Run formatting, tests, and build validation

W7 makes `send` the addressed-message command and preserves broadcast updates as `update`.

- [x] Add ID and active-agent name recipient resolution
- [x] POST addressed messages using the documented request contract
- [x] Retry transport failures and 408/500/503 with one in-memory idempotency ID
- [x] Map every documented send error without exposing message bodies
- [x] Move the existing broadcast write behavior from `send` to `update`
- [x] Make explicit top-level and command help exit successfully
- [x] Add integration, ambiguity, retry, error, secrecy, and help coverage
- [x] Update command documentation
- [x] Run formatting, tests, race tests, vet, and build validation

Name matching deliberately performs no Unicode normalization: exact trimmed matching runs first, followed only by `strings.EqualFold` when exact matching finds nothing.
