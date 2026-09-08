# Task detail command

Add a single-task view derived from the existing workstream detail response.

- [x] Add positional `task <id>` dispatch, usage, parsing, and credential selection.
- [x] Decode task detail and matching task-scoped updates from one GET request.
- [x] Print task fields and comments oldest-first with explicit unknown-task and no-comment output.
- [x] Preserve one-line, credential-redacted output behavior.
- [x] Add table-driven command and positional-argument tests.
- [x] Update `README.md`.
- [x] Run `just build` and `just test`.
- [x] Create a conventional commit without pushing or deploying.

Task fields use labeled lines; comments use tab-separated timestamp, author, and body lines. Empty descriptions and assignees render as `-`.

## Status mutation extension

- [x] Add validated `--status` PATCH behavior without changing the no-flag read path.
- [x] Generate and reuse one idempotency ID across bounded transport/408/500/503 retries.
- [x] Print the server-confirmed updated task state and map final failures clearly.
- [x] Add table-driven validation/retry/final-status tests and explicit unchanged-read coverage.
- [x] Update the README, rerun `just build` and `just test`, and create a conventional commit.

## Comment mutation extension

- [x] Add `--comment` using the existing task-scoped update endpoint.
- [x] Reject blank comments and `--comment`/`--status` combinations before any request.
- [x] Generate and reuse one server-honored idempotency ID across retries.
- [x] Print a confirmed comment result without PATCHing the task.
- [x] Add table-driven validation, retry, and task-unchanged tests.
- [x] Update the README, rerun validation, and create a conventional commit.
