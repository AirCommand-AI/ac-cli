# Activity update CLI

Implement canonical summary/detail updates and task comments, with migration guidance for removed legacy flags.

- [x] Add `update --summary [--detail] [--task]` validation, idempotent retries, and safe confirmation output.
- [x] Add `task --comment-summary [--comment-detail]` with validation and mutation exclusivity.
- [x] Reject legacy `--body` and `--comment` flags with migration guidance.
- [x] Add pull-only `activity` paging/filtering and document that it creates no wake-ups.
- [x] Update CLI help, README, and runtime adapter guidance.
- [x] Run the full CLI validation suite (`go test ./...`; `git diff --check`).
