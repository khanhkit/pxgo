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

# TC-CI-REG-007: canonical parser fuzz targets are mandatory CI and release gates.
for target in FuzzParseProxyCanonical FuzzParseNoProxyCanonical; do
  grep -Rq -- "-fuzz.*${target}" .github/workflows/ci.yml .github/workflows/release.yml ||
    bad "fuzz gate missing for ${target}"
done
grep -q 'FuzzParseProxyCanonical' .github/workflows/ci.yml &&
  grep -q 'FuzzParseNoProxyCanonical' .github/workflows/ci.yml &&
  grep -q 'FuzzParseProxyCanonical' .github/workflows/release.yml &&
  grep -q 'FuzzParseNoProxyCanonical' .github/workflows/release.yml &&
  ok "canonical parser fuzz targets gate CI and release"

# TC-CI-REG-008: manual exact-ref native Windows SSPI verification is schedulable.
grep -q 'workflow_dispatch:' .github/workflows/ci.yml || bad "CI lacks workflow_dispatch"
grep -q 'verification_ref:' .github/workflows/ci.yml || bad "manual CI lacks exact verification_ref input"
grep -q 'windows-native-sspi:' .github/workflows/ci.yml || bad "independent native Windows SSPI job missing"
grep -q 'PXGO_SSPI_NATIVE' .github/workflows/ci.yml || bad "native Windows SSPI gate missing"
grep -Fq "TCSSPIWIN(INT008|SOAK009)" .github/workflows/ci.yml || bad "native Windows SSPI testcase guard missing"

# TC-CI-REG-009: real AD verification has an explicit protected self-hosted contract.
grep -q 'pxgo-ad' .github/workflows/ci.yml || bad "protected AD runner label missing"
grep -q 'PXGO_SSPI_AD_PROXY_HOST' .github/workflows/ci.yml || bad "real AD proxy host contract missing"
grep -q 'TCSSPIWINAD010' .github/workflows/ci.yml || bad "real AD Kerberos testcase guard missing"
grep -q 'verify-windows-native:' .github/workflows/release.yml || bad "release native Windows SSPI gate missing"
grep -q 'windows-domain-sspi:' .github/workflows/release.yml || bad "release real AD SSPI gate missing"
grep -q 'needs: \[verify-exact-sha, verify-windows-native, windows-domain-sspi\]' .github/workflows/release.yml || bad "GoReleaser is not gated on native Windows + real AD verification"

# TC-CI-REG-010: the minimum Go toolchain must include all reachable stdlib
# security fixes currently required by the project.
required_go_major=1
required_go_minor=25
required_go_patch=13
go_version="$(awk '$1 == "go" { print $2; exit }' go.mod)"
IFS=. read -r go_major go_minor go_patch <<<"$go_version"
go_patch="${go_patch:-0}"
if (( go_major < required_go_major ||
      (go_major == required_go_major && go_minor < required_go_minor) ||
      (go_major == required_go_major && go_minor == required_go_minor && go_patch < required_go_patch) )); then
  bad "Go toolchain minimum ${go_version} is below security floor 1.25.13"
else
  ok "Go toolchain minimum satisfies security floor 1.25.13"
fi

exit "$fail"
