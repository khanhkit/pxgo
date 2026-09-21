#!/usr/bin/env bash
set -euo pipefail

release=.github/workflows/release.yml
fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "PASS: $*"; }

grep -q '^  release-candidate:' "$release" || bad 'release-candidate job missing'
grep -q '^  verify-release-artifacts:' "$release" || bad 'verify-release-artifacts job missing'
grep -q '^  promote-release:' "$release" || bad 'promote-release job missing'

grep -Eq 'release --clean .*--skip=publish|release .*--skip=publish.*--clean' "$release" || bad 'candidate GoReleaser run does not skip publish'
grep -Fq 'release-candidate-${{ github.sha }}' "$release" || bad 'candidate artifact is not keyed to exact SHA'
grep -q 'actions/upload-artifact@' "$release" || bad 'candidate dist is not staged with upload-artifact'
grep -q 'actions/download-artifact@' "$release" || bad 'staged candidate is not downloaded for verification/promotion'

for runner in ubuntu-latest ubuntu-24.04-arm windows-latest macos-15; do
  grep -Fq "runner: ${runner}" "$release" || bad "native artifact runner missing: ${runner}"
done
grep -Fq 'go run ./scripts/verify-release-artifact.go' "$release" || bad 'exact archive verifier is not executed'

grep -Eq 'needs:.*release-candidate.*verify-release-artifacts|needs:.*verify-release-artifacts.*release-candidate' "$release" || bad 'promotion is not gated on candidate plus native verification'
grep -Fq 'sha256sum --check checksums.txt' "$release" || bad 'promotion does not re-verify staged checksums'
grep -Fq 'gh release create' "$release" || bad 'promotion does not create release from staged artifacts'

count=$(grep -c 'goreleaser/goreleaser-action@' "$release" || true)
[[ "$count" -eq 1 ]] || bad "expected exactly one GoReleaser action, found ${count}"

grep -q 'subject-checksums:' "$release" || bad 'provenance attestation missing after promotion'
grep -A5 '^  update-homebrew-tap:' "$release" | grep -Eq 'needs:.*promote-release' || bad 'Homebrew update is not downstream of promotion'

[[ -f scripts/verify-release-artifact.go ]] || bad 'release artifact runtime verifier missing'

if [[ "$fail" -eq 0 ]]; then
  ok 'artifact-first release contract present'
fi
exit "$fail"
