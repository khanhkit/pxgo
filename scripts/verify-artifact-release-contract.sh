#!/usr/bin/env bash
set -euo pipefail

release=.github/workflows/release.yml
fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "PASS: $*"; }

grep -q '^  release-candidate:' "$release" || bad 'release-candidate job missing'
grep -q '^  verify-release-artifacts:' "$release" || bad 'verify-release-artifacts job missing'
grep -q '^  promote-release:' "$release" || bad 'promote-release job missing'
grep -q '^  dry-run-promotion-proof:' "$release" || bad 'dry-run promotion proof job missing'

grep -q 'workflow_dispatch:' "$release" || bad 'manual hosted dry-run trigger missing'
grep -q 'verification_ref:' "$release" || bad 'manual dry-run lacks exact verification_ref input'
grep -q 'inject_verifier_failure:' "$release" || bad 'manual dry-run lacks verifier failure-injection input'
grep -Fq 'v0.0.0-ap0030-dryrun-' "$release" || bad 'manual dry-run does not create a local-only release-like tag'
grep -Fq "github.event_name == 'push'" "$release" || bad 'promotion is not explicitly push-only'
grep -Fq 'needs.verify-release-artifacts.result' "$release" || bad 'dry-run does not report promotion eligibility from verifier result'

grep -Eq 'release --clean .*--skip=publish|release .*--skip=publish.*--clean' "$release" || bad 'candidate GoReleaser run does not skip publish'
grep -Fq 'release-candidate-${{ steps.tag.outputs.sha }}' "$release" || bad 'candidate upload is not keyed to exact checked-out SHA'
grep -Fq 'release-candidate-${{ needs.release-candidate.outputs.sha }}' "$release" || bad 'candidate consumers are not keyed to exact candidate SHA'
grep -q 'actions/upload-artifact@' "$release" || bad 'candidate dist is not staged with upload-artifact'
grep -q 'actions/download-artifact@' "$release" || bad 'staged candidate is not downloaded for verification/promotion'

for runner in ubuntu-latest ubuntu-24.04-arm windows-latest macos-15; do
  grep -Fq "runner: ${runner}" "$release" || bad "native artifact runner missing: ${runner}"
done
grep -Fq 'go run ./scripts/verify-release-artifact.go' "$release" || bad 'exact archive verifier is not executed'

grep -Eq 'needs:.*release-candidate.*verify-release-artifacts|needs:.*verify-release-artifacts.*release-candidate' "$release" || bad 'promotion is not gated on candidate plus native verification'
grep -Fq 'sha256sum --check checksums.txt' "$release" || bad 'promotion does not re-verify staged checksums'
grep -Fq '.type == "Archive" or .type == "SBOM" or .type == "Checksum"' "$release" || bad 'promotion asset set is not derived from GoReleaser publishable artifact metadata'
grep -Fq -- '--notes-file dist/CHANGELOG.md' "$release" || bad 'promotion does not preserve GoReleaser changelog notes'
grep -Fq 'gh release create' "$release" || bad 'promotion does not create release from staged artifacts'
grep -Fq -- '--draft' "$release" || bad 'dry-run promotion does not use a draft release'
grep -Fq -- '--latest=false' "$release" || bad 'dry-run draft could affect latest release state'
grep -Fq -- '--target "$SHA"' "$release" || bad 'dry-run draft is not bound to exact candidate SHA'
grep -Fq 'gh release download "$TAG"' "$release" || bad 'dry-run promotion does not download uploaded assets for byte verification'
grep -Fq 'cmp --silent' "$release" || bad 'dry-run promotion does not compare uploaded bytes to candidate bytes'
grep -Fq -- '--cleanup-tag' "$release" || bad 'dry-run promotion does not clean temporary release tag'

count=$(grep -c 'goreleaser/goreleaser-action@' "$release" || true)
[[ "$count" -eq 1 ]] || bad "expected exactly one GoReleaser action, found ${count}"

grep -q 'subject-checksums:' "$release" || bad 'provenance attestation missing after promotion'
grep -A5 '^  update-homebrew-tap:' "$release" | grep -Eq 'needs:.*promote-release' || bad 'Homebrew update is not downstream of promotion'

[[ -f scripts/verify-release-artifact.go ]] || bad 'release artifact runtime verifier missing'

if [[ "$fail" -eq 0 ]]; then
  ok 'artifact-first release contract present'
fi
exit "$fail"
