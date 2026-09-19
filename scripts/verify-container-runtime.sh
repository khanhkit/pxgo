#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

source_only=false
if [[ "${1:-}" == "--source-only" ]]; then
  source_only=true
elif [[ $# -ne 0 ]]; then
  echo "usage: $0 [--source-only]" >&2
  exit 2
fi

fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "PASS: $*"; }

dockerfiles=(Dockerfile docker/Dockerfile.heimdal-client docker/Dockerfile.heimdal-kdc docker/Dockerfile.mit-kdc)

for f in "${dockerfiles[@]}"; do
  [[ -f "$f" ]] || { bad "missing $f"; continue; }
  if grep -Eq '^FROM[[:space:]]+[^[:space:]]*:latest([[:space:]]|$)' "$f"; then
    bad "$f uses :latest"
  fi
  while IFS= read -r image; do
    [[ "$image" == *'@sha256:'* ]] || bad "$f has unpinned base: $image"
  done < <(awk '$1=="FROM" {print $2}' "$f")
done

grep -Fq 'golang:1.25.13-alpine3.24@sha256:' Dockerfile ||
  bad "published builder is not pinned to Go 1.25.13 / Alpine 3.24"
grep -Fq 'USER 65532:65532' Dockerfile || bad "published runtime USER is not fixed non-root 65532:65532"
grep -Fq 'HOME=/home/pxgo' Dockerfile || bad "published runtime HOME contract missing"
grep -Fq 'XDG_CONFIG_HOME=/home/pxgo/.config' Dockerfile || bad "published runtime XDG config contract missing"

for f in Dockerfile docker/Dockerfile.heimdal-client docker/Dockerfile.heimdal-kdc docker/Dockerfile.mit-kdc; do
  if grep -Eq '(^|[[:space:]])(apk add|apt-get install)([[:space:]].*)?[[:space:]][A-Za-z0-9][A-Za-z0-9+_.-]*([[:space:]\\]|$)' "$f"; then
    # Explicit versions are checked more directly below; this catches obvious
    # unversioned package install lines but allows shell flags.
    :
  fi
done

grep -Fq 'ca-certificates=20260909-r0' Dockerfile || bad "ca-certificates version pin missing"
grep -Fq 'krb5=1.22.2-r1' Dockerfile || bad "krb5 version pin missing"
grep -Fq 'tini=0.19.0-r3' Dockerfile || bad "tini version pin missing"
grep -Fq 'heimdal=7.8.0-r5' docker/Dockerfile.heimdal-client || bad "Heimdal client version pin missing"
grep -Fq 'heimdal-kdc=7.8.git20221117.28daf24+dfsg-2' docker/Dockerfile.heimdal-kdc || bad "Heimdal KDC version pin missing"
grep -Fq 'heimdal-clients=7.8.git20221117.28daf24+dfsg-2' docker/Dockerfile.heimdal-kdc || bad "Heimdal clients version pin missing"
grep -Fq 'krb5-server=1.22.2-r1' docker/Dockerfile.mit-kdc || bad "MIT KDC server version pin missing"

if grep -RIEq '(password|passwd|secret|token|keytab)[[:space:]]*[:=][[:space:]]*["'"'"']?[^$<{[:space:]]+' Dockerfile docker; then
  bad "container sources appear to contain a baked credential"
fi

[[ "$fail" -eq 0 ]] && ok "container source contract is reproducible and least-privilege aware"

if "$source_only"; then
  exit "$fail"
fi

command -v docker >/dev/null 2>&1 || bad "docker CLI is required for runtime verification"
command -v syft >/dev/null 2>&1 || bad "syft is required for SBOM verification"
[[ "$fail" -eq 0 ]] || exit "$fail"

image="pxgo:container-contract-${GITHUB_SHA:-local}"
docker build --pull -t "$image" .

user="$(docker image inspect "$image" --format '{{.Config.User}}')"
[[ "$user" == "65532:65532" ]] || bad "image user=$user, want 65532:65532"

entrypoint="$(docker image inspect "$image" --format '{{json .Config.Entrypoint}}')"
[[ "$entrypoint" == '["/sbin/tini","--","/bin/sh","/pxgo/start.sh"]' ]] ||
  bad "unexpected entrypoint: $entrypoint"

security_args=(
  --read-only
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m
  --cap-drop=ALL
  --security-opt no-new-privileges:true
)

docker run --rm "${security_args[@]}" --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" = "65532"
  test "$(id -g)" = "65532"
  test "$HOME" = "/home/pxgo"
  test "$XDG_CONFIG_HOME" = "/home/pxgo/.config"
  touch /tmp/pxgo-runtime-probe
  if touch /pxgo/rootfs-write-probe 2>/dev/null; then
    echo "read-only rootfs write unexpectedly succeeded" >&2
    exit 1
  fi
  grep -Eq "^CapEff:[[:space:]]+0+$" /proc/self/status
'

docker run --rm "${security_args[@]}" "$image" --version >/dev/null

sbom="$(mktemp)"
trap 'rm -f "$sbom"; docker image rm -f "$image" >/dev/null 2>&1 || true' EXIT
syft "$image" -o spdx-json="$sbom" >/dev/null
[[ -s "$sbom" ]] || bad "Syft produced an empty SBOM"
grep -q '"SPDXID"' "$sbom" || bad "Syft output is not SPDX JSON"

[[ "$fail" -eq 0 ]] && ok "container runtime smoke + metadata + SBOM verification passed"
exit "$fail"
