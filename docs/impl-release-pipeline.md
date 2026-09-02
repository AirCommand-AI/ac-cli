# Release pipeline

Implement T6's CI and tagged GitHub Release pipeline, add version reporting, and publish `v0.1.0`.

- [x] Add `ac-cli --version` with link-time version injection
- [x] Add minimal push and pull-request CI for tests and vet
- [x] Add tag-triggered release workflow for four static targets
- [x] Generate and publish `SHA256SUMS` with exact asset names
- [x] Validate tests, vet, version output, and all four local cross-builds
- [ ] Commit and push the release plumbing to `main`
- [ ] Tag and push `v0.1.0`
- [ ] Confirm the release workflow and all five assets
- [ ] Record the published binary URLs and SHA-256 values
