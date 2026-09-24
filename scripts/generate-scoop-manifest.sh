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
  awk -v asset="$asset" '$2 == asset { print $1; found=1; exit } END { if (!found) exit 1 }' "${checksums}"
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
printf '%s
' "${output}"
