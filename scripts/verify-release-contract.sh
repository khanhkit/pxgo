#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "PASS: $*"; }

# TC-CI-REG-001: all third-party actions are immutable SHA pins.
while IFS= read -r line; do
  ref="${line#*@}"
  ref="${ref%%[[:space:]]*}"
  if [[ ! "$ref" =~ ^[0-9a-f]{40}$ ]]; then
    bad "floating GitHub Action ref: $line"
  fi
done < <(grep -RhE '^[[:space:]]*-[[:space:]]+uses:[[:space:]]+[^./][^@[:space:]]+/[^@[:space:]]+@' .github/workflows || true)
[[ "$fail" -eq 0 ]] && ok "GitHub Actions use immutable SHA refs"

# TC-CI-REG-002: the artifact-producing GoReleaser step must consume reviewed
# module metadata. An isolated pre-release verify job may run tidy + diff because
# that workspace is not reused by the build/publish job.
if grep -Eq '(^|[[:space:]])go mod tidy([[:space:]]|$)' .goreleaser.yaml; then
  bad "GoReleaser mutates go.mod/go.sum"
else
  ok "GoReleaser consumes reviewed module metadata"
fi

# TC-CI-REG-003: release verifies exact tag SHA and gates publish on verification.
grep -q 'verify-exact-sha' .github/workflows/release.yml || bad "release missing verify-exact-sha job"
grep -q 'needs:.*verify-exact-sha' .github/workflows/release.yml || bad "GoReleaser is not gated on verify-exact-sha"
grep -q 'github.sha' .github/workflows/release.yml || bad "exact-SHA verification does not reference github.sha"

# TC-CI-REG-004: vulnerability scan is a required CI/release command.
grep -Rq 'govulncheck' .github/workflows/ci.yml .github/workflows/release.yml Makefile || bad "govulncheck gate missing"

# TC-CI-REG-005: GoReleaser version must be exact, never latest.
if grep -Eq 'version:[[:space:]]*latest' .github/workflows/release.yml; then
  bad "GoReleaser uses moving latest"
else
  ok "GoReleaser version is exact"
fi

# TC-CI-REG-006: published archives have SBOMs and signed provenance.
grep -q '^sboms:' .goreleaser.yaml || bad "GoReleaser SBOM generation missing"
grep -q 'actions/attest@' .github/workflows/release.yml || bad "signed provenance attestation missing"
grep -q 'subject-checksums:[[:space:]]*dist/checksums.txt' .github/workflows/release.yml || bad "attestation is not bound to release checksums"
if grep -q '^sboms:' .goreleaser.yaml &&
   grep -q 'actions/attest@' .github/workflows/release.yml &&
   grep -q 'subject-checksums:[[:space:]]*dist/checksums.txt' .github/workflows/release.yml; then
  ok "SBOM and signed provenance contract present"
fi

exit "$fail"
