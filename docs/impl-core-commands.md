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
- [x] Keep retries limited to transport failures, never HTTP status responses
- [x] Add multi-agent selection and strict response-contract tests
- [x] Run formatting, tests, and build validation
