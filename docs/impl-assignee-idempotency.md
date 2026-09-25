# Idempotency key for task reassignment

Issue: [ac-cli#10](https://github.com/AirCommand-AI/ac-cli/issues/10)

## Problem

`aircom task <id> --assignee` sent `{"assignee": …}` with no idempotency ID,
while `--status` and `--comment` each send one. The request is retried on
transport failures and 408/500/503, and the server (ac-dashboard#5) now applies
a keyed task patch once — but only if a key is sent. A retried reassignment
could therefore be applied twice and post its "Reassigned from … to …" update
twice.

## Change

- `taskAssigneeRequest` gains `idempotencyId`.
- `setTaskAssignee` generates one ID per invocation with
  `secrets.IdempotencyID(a.randomReader())`, the same as `setTaskStatus`, and
  marshals it into the single payload that `messageAPIRequest` reuses across its
  retries. If the ID cannot be generated the command stops before any request:
  "Unable to generate a task reassignment idempotency ID."
- No change to notifications or output.

## Tests

`internal/app/task_detail_test.go`: the payload carries the key; for 408, 500
and 503 the retry sends the identical body with the same key; with no
randomness available the command fails before making a request.

## Validation

`just test` (`go test ./...`), `go vet ./...` and `git diff --check` pass.
