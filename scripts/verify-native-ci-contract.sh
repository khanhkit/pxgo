#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "native-ci-contract: $*" >&2
  exit 1
}

ci=.github/workflows/ci.yml
krb=internal/kerberos/kerberos_integration_test.go
runner=scripts/run-kerberos-integration.sh

grep -Fq 'windows-latest' "$ci" || fail "Windows native gate missing"
grep -Fq 'ubuntu-24.04-arm' "$ci" || fail "Linux arm64 native runner missing"
grep -Fq 'macos-15' "$ci" || fail "macOS native runner missing"

grep -Fq 'Large transfer integrity' "$ci" || fail "dedicated large-transfer gate missing"
grep -Fq 'Test(LargeHTTPAndHTTPS|LargeDataMultipleSizes|MixedConcurrentLargeTransfers)' "$ci" ||
  fail "large-transfer gate does not select the integrity tests"

grep -Fq './scripts/run-kerberos-integration.sh' "$ci" || fail "live Kerberos fixture gate missing"
grep -Fq "go test -run '^$' -tags=kerberos_integration ./internal/kerberos" "$ci" ||
  fail "build-tagged Kerberos compile-only gate missing"

if grep -Eq 't\.Skip(f)?\(' "$krb"; then
  fail "Kerberos integration suite may still green by skipping missing fixtures"
fi

[[ -x "$runner" ]] || fail "Kerberos integration runner missing or not executable"
bash -n "$runner"

echo "native-ci-contract: PASS"
