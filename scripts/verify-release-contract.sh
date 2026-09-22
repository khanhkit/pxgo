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

# TC-CI-REG-008B: AP-ISS-0006 native WinHTTP cancellation/handle fixtures must
# execute on hosted Windows instead of being silently skipped behind their env guard.
grep -Fq 'PXGO_WINHTTP_NATIVE' .github/workflows/ci.yml || bad "native WinHTTP fixture environment gate missing"
grep -Fq "TCWINPAC(INT009|NEG010|NEG014|SOAK011)" .github/workflows/ci.yml || bad "native WinHTTP testcase guard missing"
grep -Fq 'TestTCWINPACSOAK011NativeHandleCountReturnsNearBaseline' .github/workflows/ci.yml || bad "native WinHTTP handle-soak fixture missing"

# TC-CI-REG-009: real AD verification is preserved as an explicit manual,
# protected self-hosted workflow, but it is intentionally not a normal CI or
# release blocker. External domain infrastructure is tracked in docs/TODO.md.
ad_workflow=".github/workflows/real-ad-verification.yml"
[[ -f "$ad_workflow" ]] || bad "manual real AD verification workflow missing"
if [[ -f "$ad_workflow" ]]; then
  grep -q 'workflow_dispatch:' "$ad_workflow" || bad "real AD workflow is not manual-only"
  grep -q 'runs-on: \[self-hosted, windows, x64, pxgo-ad\]' "$ad_workflow" || bad "protected AD runner labels missing"
  grep -q 'environment: pxgo-ad' "$ad_workflow" || bad "protected AD environment missing"
  grep -q 'PXGO_SSPI_AD_PROXY_HOST' "$ad_workflow" || bad "real AD proxy host contract missing"
  grep -q 'TCSSPIWINAD010' "$ad_workflow" || bad "real AD Kerberos testcase guard missing"
fi
if grep -q 'windows-domain-sspi:' .github/workflows/ci.yml; then
  bad "real AD job must not block normal CI"
fi
if grep -q 'windows-domain-sspi:' .github/workflows/release.yml; then
  bad "real AD job must not block release"
fi
grep -q 'verify-windows-native:' .github/workflows/release.yml || bad "release native Windows SSPI gate missing"
grep -q 'needs: \[verify-exact-sha, verify-windows-native\]' .github/workflows/release.yml || bad "GoReleaser is not gated on exact SHA + native Windows verification"

# TC-CI-REG-009B: the protected AD gate is reproducibly bootstrap-able from one
# disposable Windows Server VM. The runner binary itself is pinned by version
# and digest; one-time registration credentials are supplied at runtime only.
ad_bootstrap="scripts/bootstrap-pxgo-ad.ps1"
[[ -f "$ad_bootstrap" ]] || bad "single-VM protected AD bootstrap missing"
if [[ -f "$ad_bootstrap" ]]; then
  grep -Fq "[string]\$RunnerVersion = '2.337.0'" "$ad_bootstrap" || bad "AD runner version is not pinned"
  grep -Fq "1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc" "$ad_bootstrap" || bad "AD runner SHA-256 pin missing"
  grep -Fq 'Install-ADDSForest' "$ad_bootstrap" || bad "AD forest bootstrap missing"
  grep -Fq 'setspn.exe -U -S' "$ad_bootstrap" || bad "HTTP SPN bootstrap missing"
  grep -Fq -- '--runasservice' "$ad_bootstrap" || bad "AD runner service install missing"
  grep -Fq -- '--disableupdate' "$ad_bootstrap" || bad "pinned AD runner can auto-update unexpectedly"
  grep -Fq 'PXGO_RUNNER_TOKEN' "$ad_bootstrap" || bad "one-time runner token contract missing"
  grep -Fq 'Validate protected AD bootstrap PowerShell' .github/workflows/ci.yml || bad "Windows CI does not parse-check AD bootstrap"
  grep -Fq 'System.Management.Automation.Language.Parser]::ParseFile' .github/workflows/ci.yml || bad "Windows CI PowerShell parser contract missing"
  if grep -Eq 'gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]+' "$ad_bootstrap"; then
    bad "hard-coded GitHub credential detected in AD bootstrap"
  fi
  [[ "$fail" -eq 0 ]] && ok "single-VM protected AD bootstrap contract present"
fi

ad_dispatch="scripts/dispatch-real-ad-verification.sh"
[[ -f "$ad_dispatch" && -x "$ad_dispatch" ]] || bad "real AD dispatch helper missing or not executable"
if [[ -f "$ad_dispatch" ]]; then
  bash -n "$ad_dispatch" || bad "real AD dispatch helper has invalid shell syntax"
  grep -Fq 'real-ad-verification.yml' "$ad_dispatch" || bad "real AD dispatch helper does not use manual AD workflow"
  grep -Fq 'pending_deployments' "$ad_dispatch" || bad "protected environment approval orchestration missing"
  grep -Fq 'pxgo-ad' "$ad_dispatch" || bad "dispatch helper does not verify pxgo-ad runner label"
  [[ "$fail" -eq 0 ]] && ok "protected AD dispatch orchestration present"
fi

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

# Distribution identity contract.
grep -A4 '^release:' .goreleaser.yaml | grep -q 'owner: khanhkit' || bad "GitHub release owner is not khanhkit"
grep -q 'BASE_URL="https://github.com/khanhkit/pxgo/releases/download/' .github/workflows/release.yml || bad "Homebrew release URL does not target fork"
grep -q 'github.com/khanhkit/homebrew-tap.git' .github/workflows/release.yml || bad "Homebrew tap target is not fork-owned"
grep -q "vars.PXGO_HOMEBREW_TAP_ENABLED == 'true'" .github/workflows/release.yml || bad "Homebrew publication lacks explicit opt-in gate"
[[ "$(awk '$1 == "module" {print $2; exit}' go.mod)" == 'github.com/khanhkit/pxgo' ]] || bad "Go module identity is not the authoritative fork"
grep -q '^winget:' .goreleaser.yaml && bad "legacy WinGet publication config must remain retired"
grep -q 'WINGET_TOKEN' .github/workflows/release.yml && bad "release workflow still references retired WinGet credentials"
legacy_owner='pavel''simo'
if git grep -n -i "$legacy_owner" -- . >/dev/null 2>&1; then
  bad "legacy repository/package owner marker is still present"
fi
[[ -f docs/distribution-identity.md ]] || bad "distribution identity decision document missing"

dependabot=.github/dependabot.yml
[[ -f "$dependabot" ]] || bad "Dependabot configuration missing"
if [[ -f "$dependabot" ]]; then
  for ecosystem in gomod github-actions docker; do
    grep -q "package-ecosystem: \"$ecosystem\"" "$dependabot" || bad "Dependabot missing $ecosystem"
  done
  [[ "$(grep -c 'interval: \"weekly\"' "$dependabot")" -ge 3 ]] || bad "Dependabot ecosystems are not on weekly cadence"
fi

exit "$fail"
