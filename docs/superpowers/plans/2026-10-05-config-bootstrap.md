# Config Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Generate and incrementally upgrade a self-documenting `pxgo.ini`, preserving user intent and importing proxy state with precedence `pxgo.ini > px.ini > system`, with PAC preferred over manual proxy.

**Architecture:** Add a small config-document bootstrap/migration layer around the existing parser instead of replacing runtime config resolution. Existing files are minimally patched with timestamped backup + atomic write; fresh files use a documented template. System proxy import is explicit when an INI already exists and automatic only when neither PxGo nor legacy Px config exists.

**Tech Stack:** Go standard library and existing PxGo config/systemproxy packages.

**Spec:** User-approved design in project conversation, 2026-10-05.

## Global Constraints
- Fresh generated `listen = 0.0.0.0`.
- Fresh generated `auto_update = install`, `update_interval = 24h`, `update_channel = stable`, `install_provider = auto`.
- Optional/disabled features such as DNS remain commented with inline guidance.
- Normal bootstrap precedence: `pxgo.ini > px.ini > system proxy > DIRECT`.
- Within discovered system routing, PAC wins over manual HTTP proxy.
- Existing explicit values are never overwritten by new defaults.
- Any mutation of an existing user INI requires timestamped backup first and atomic write.
- `--apply-system-proxy` explicitly imports current system routing into canonical `pxgo.ini` while preserving unrelated settings.
- Never persist passwords/tokens from environment or runtime credentials.

## Review Focus
- First run with no config must not create an unauthenticated public proxy silently without warning in the generated file.
- A legacy `px.ini` must remain untouched and outrank system discovery.
- Dynamic WPAD/per-scheme Windows routing must not be flattened lossily.
- Schema migration must be idempotent and preserve unknown keys/comments/order.
- Backup failure must prevent mutation of an existing INI.

---

### Task 1: Config document bootstrap and safe migration
**Files:** modify `internal/config`; tests in `internal/config`.
- RED tests for fresh documented template, default auto-update install, schema append, idempotence, backup-before-write, legacy preservation.
- Implement minimal document patch/write helpers.
- GREEN targeted config tests.

### Task 2: System proxy import and startup precedence
**Files:** modify startup/config integration and `internal/systemproxy` only as needed; tests at main/config boundary.
- RED tests for `pxgo.ini > px.ini > system`, PAC > manual, and explicit `--apply-system-proxy`.
- Implement startup bootstrap/import wiring without altering runtime discovery semantics.
- GREEN targeted tests.

### Task 3: Documentation, E2E, and verification
**Files:** update README/config docs/sample; add first-run E2E coverage.
- Verify generated INI can be consumed on second launch and remains byte-stable.
- Run gofmt, targeted tests, `go test ./... -count=1`, vet, and existing E2E/release contract where applicable.
- Ponytail simplification review before final commit.
