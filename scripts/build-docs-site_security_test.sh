#!/usr/bin/env bash
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/scripts"
cp "$repo/scripts/build-docs-site.mjs" "$tmp/scripts/"
cp -R "$repo/docs" "$tmp/docs"

payload='docs.example.test/" onmouseover="alert(1)'
printf '%s\n' "$payload" > "$tmp/docs/CNAME"

(
  cd "$tmp"
  node scripts/build-docs-site.mjs >/dev/null
)

html="$tmp/dist/docs-site/index.html"
[[ -s "$html" ]]

if grep -Fq 'onmouseover="alert(1)' "$html"; then
  echo "FAIL: docs builder emitted attacker-controlled attribute syntax" >&2
  exit 1
fi

grep -Fq '&quot; onmouseover=&quot;alert(1)' "$html" ||
  { echo "FAIL: malicious CNAME was not escaped for attribute context" >&2; exit 1; }

echo "PASS: docs builder escapes attacker-controlled HTML attributes"
