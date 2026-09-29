#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

icon="assets/windows/pxgo.ico"
expected="d84be2b1f38218675a6fc74693826ac3920ce576c4b749d87599230bbbb6208f"
actual="$(sha256sum "$icon" | awk '{print $1}')"
if [[ "$actual" != "$expected" ]]; then
  echo "unexpected Windows icon checksum: $actual" >&2
  exit 1
fi

for arch in amd64 arm64; do
  GOTOOLCHAIN=local go run github.com/akavel/rsrc@v0.10.2 \
    -arch "$arch" \
    -ico "$icon" \
    -o "rsrc_windows_${arch}.syso"
done
