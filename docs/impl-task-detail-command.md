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
