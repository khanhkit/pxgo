#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: generate-scoop-manifest.sh <tag> [dist-dir]}"
dist_dir="${2:-dist}"
version="${tag#v}"
checksums="${dist_dir}/checksums.txt"
output="${dist_dir}/pxgo-scoop.json"

[[ -s "${checksums}" ]] || { echo "missing checksums: ${checksums}" >&2; exit 1; }

checksum_for() {
  local asset="$1"
  local sha
  sha=$(awk -v asset="$asset" '$2 == asset { value=$1; count++ } END { if (count != 1) exit 1; print value }' "${checksums}")
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || {
    echo "invalid checksum for ${asset}: ${sha}" >&2
    return 1
  }
  printf '%s\n' "$sha"
}

amd64_asset="pxgo_windows_amd64.zip"
arm64_asset="pxgo_windows_arm64.zip"
amd64_sha="$(checksum_for "${amd64_asset}")"
arm64_sha="$(checksum_for "${arm64_asset}")"
base_url="https://github.com/khanhkit/pxgo/releases/download/${tag}"

jq -n   --arg version "${version}"   --arg base "${base_url}"   --arg amd64_asset "${amd64_asset}"   --arg arm64_asset "${arm64_asset}"   --arg amd64_sha "${amd64_sha}"   --arg arm64_sha "${arm64_sha}"   '{
    version: $version,
    description: "HTTP/HTTPS proxy with NTLM, Kerberos and corporate proxy authentication",
    homepage: "https://github.com/khanhkit/pxgo",
    license: "MIT",
    architecture: {
      "64bit": {
        url: ($base + "/" + $amd64_asset),
        hash: $amd64_sha
      },
      "arm64": {
        url: ($base + "/" + $arm64_asset),
        hash: $arm64_sha
      }
    },
    bin: "pxgo.exe",
    checkver: { github: "https://github.com/khanhkit/pxgo" },
    autoupdate: {
      architecture: {
        "64bit": { url: "https://github.com/khanhkit/pxgo/releases/download/v$version/pxgo_windows_amd64.zip" },
        "arm64": { url: "https://github.com/khanhkit/pxgo/releases/download/v$version/pxgo_windows_arm64.zip" }
      }
    }
  }' > "${output}"

jq -e '.version and .architecture["64bit"].hash and .architecture.arm64.hash and .bin == "pxgo.exe"' "${output}" >/dev/null

manifest_asset="$(basename "${output}")"
manifest_sha="$(sha256sum "${output}" | awk '{print $1}')"
checksum_tmp="$(mktemp "${checksums}.tmp.XXXXXX")"
trap 'rm -f "${checksum_tmp}"' EXIT
awk -v asset="${manifest_asset}" '$2 != asset' "${checksums}" > "${checksum_tmp}"
printf '%s  %s\n' "${manifest_sha}" "${manifest_asset}" >> "${checksum_tmp}"
mv "${checksum_tmp}" "${checksums}"
trap - EXIT

printf '%s\n' "${output}"
