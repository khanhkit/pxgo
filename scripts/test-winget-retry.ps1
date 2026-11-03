# Fully mocked Windows CI tests: no live credentials, network, or submissions.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$syncWorkflow = Get-Content -Raw '.github/workflows/winget-sync.yml'
if ($syncWorkflow -notmatch 'workflow_dispatch:' -or
    $syncWorkflow -match 'promote-release:|release-candidate:|goreleaser|gh release create' -or
    $syncWorkflow -notmatch 'git merge-base --is-ancestor' -or
    $syncWorkflow -notmatch 'submit-winget-release.ps1') {
    throw 'WinGet retry-only workflow contract is missing or contains release promotion'
}
. (Join-Path $PSScriptRoot 'winget-manifest-integrity.ps1')

$global:MockVersionContent = @'
PackageIdentifier: KhanhKit.PxGo
PackageVersion: 0.9.0
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.12.0
'@
$global:MockInstallerContent = @'
PackageIdentifier: KhanhKit.PxGo
PackageVersion: 0.9.0
InstallerType: zip
NestedInstallerType: portable
NestedInstallerFiles:
- RelativeFilePath: pxgo.exe
  PortableCommandAlias: pxgo
Installers:
- Architecture: x64
  InstallerUrl: https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_amd64.zip
  InstallerSha256: E4001F1FC62E26472AC96FAE5D5C3DCEE5BD23789E671D2DF0884B6070C8261F
- Architecture: arm64
  InstallerUrl: https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_arm64.zip
  InstallerSha256: 480E692EC7868DF13CD159632B726D80A4D4E212056C6864D61A56617F55E38B
ManifestType: installer
ManifestVersion: 1.12.0
ReleaseDate: 2026-10-07
'@
$global:MockRelease = [pscustomobject]@{
    tag_name = 'v0.9.0'
    draft = $false
    prerelease = $false
    published_at = '2026-10-07T01:03:00Z'
    assets = @(
        [pscustomobject]@{
            name = 'pxgo_windows_amd64.zip'
            digest = 'sha256:e4001f1fc62e26472ac96fae5d5c3dcee5bd23789e671d2df0884b6070c8261f'
            browser_download_url = 'https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_amd64.zip'
            size = 7888477
            state = 'uploaded'
        },
        [pscustomobject]@{
            name = 'pxgo_windows_arm64.zip'
            digest = 'sha256:480e692ec7868df13cd159632b726d80a4d4e212056c6864d61a56617f55e38b'
            browser_download_url = 'https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_arm64.zip'
            size = 7115281
            state = 'uploaded'
        }
    )
}

function Assert-ThrowsLike {
    param([scriptblock]$Test, [string]$Pattern)
    $caught = $false
    try {
        & $Test | Out-Null
    }
    catch {
        if ($_.Exception.Message -notlike "*$Pattern*") {
            throw "Expected error containing $Pattern but got $($_.Exception.Message)"
        }
        $caught = $true
    }
    if (-not $caught) {
        throw "Expected failure containing $Pattern, but command succeeded"
    }
}

$expected = Get-ExpectedWingetInstallers -Release $global:MockRelease -Tag 'v0.9.0'
if (-not (Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent $global:MockInstallerContent -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected)) {
    throw 'verified WinGet source manifest did not match the published release'
}
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('E4001F1F', 'A4001F1F')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'conflicts'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('github.com/khanhkit/pxgo/releases', 'github.com/attacker/pxgo/releases')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'conflicts'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('PackageVersion: 0.9.0', 'PackageVersion: 0.9.1')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'identity'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('- Architecture: arm64', '- Architecture: x64')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'duplicate'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent + [char]10 + 'PackageVersion: 0.9.0') -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'duplicate'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('NestedInstallerType: portable', 'NestedInstallerType: exe')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'packaging semantics'
# YAML supports an optional space before ':'. A second root key written
# that way must not be ignored by our subset parser and result in false green.
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('InstallerType: zip', "InstallerType: zip`nInstallerType : exe")) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'unsupported WinGet manifest root syntax'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('InstallerType: zip', "InstallerType: zip`nInstallerSwitches: /quiet")) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'unsupported WinGet manifest root field'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent ($global:MockVersionContent + "`nExtraSchema: trust-bypass") -InstallerContent $global:MockInstallerContent -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'unsupported WinGet manifest root field'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('RelativeFilePath: pxgo.exe', 'RelativeFilePath: evil.exe')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'nested executable'
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $global:MockVersionContent -InstallerContent ($global:MockInstallerContent.Replace('NestedInstallerFiles:', 'NestedInstallerFiles:' + [char]10 + '- RelativeFilePath: evil.exe')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.9.0' -ExpectedInstallers $expected
} 'nested executable'
$alteredRelease = $global:MockRelease | Select-Object -Property *
$alteredRelease.assets = @($global:MockRelease.assets[0])
Assert-ThrowsLike {
    Get-ExpectedWingetInstallers -Release $alteredRelease -Tag 'v0.9.0'
} 'exactly one'
Write-Host 'PASS: valid x64/arm64 manifests and injected SHA, URL, identity, duplicate-arch, duplicate-field, packaging, nested executable, and release-asset conflicts'

# Published 0.7.1 used per-architecture nested executable mappings, while
# 0.8.1+ use a top-level shared mapping. Both forms must be checked exactly.
$legacyInstallerContent = @'
PackageIdentifier: KhanhKit.PxGo
PackageVersion: 0.7.1
InstallerType: zip
NestedInstallerType: portable
Installers:
- Architecture: x64
  NestedInstallerFiles:
  - RelativeFilePath: pxgo.exe
    PortableCommandAlias: pxgo
  InstallerUrl: https://github.com/khanhkit/pxgo/releases/download/v0.7.1/pxgo_windows_amd64.zip
  InstallerSha256: 2BC91ED0CF23C3B1755008192776CB597BA6D145598F87977A3DDA2F2888CEFC
- Architecture: arm64
  NestedInstallerFiles:
  - RelativeFilePath: pxgo.exe
    PortableCommandAlias: pxgo
  InstallerUrl: https://github.com/khanhkit/pxgo/releases/download/v0.7.1/pxgo_windows_arm64.zip
  InstallerSha256: 4A686C273293D9535933DF5BC5683E8BBE198D51C29071430DDD29210547AF7E
ManifestType: installer
ManifestVersion: 1.12.0
'@
$legacyExpected = @{
    x64 = @{
        Url = 'https://github.com/khanhkit/pxgo/releases/download/v0.7.1/pxgo_windows_amd64.zip'
        Sha256 = '2bc91ed0cf23c3b1755008192776cb597ba6d145598f87977a3dda2f2888cefc'
    }
    arm64 = @{
        Url = 'https://github.com/khanhkit/pxgo/releases/download/v0.7.1/pxgo_windows_arm64.zip'
        Sha256 = '4a686c273293d9535933df5bc5683e8bbe198d51c29071430ddd29210547af7e'
    }
}
$legacyVersionContent = $global:MockVersionContent.Replace('0.9.0', '0.7.1')
Assert-WingetInstallerManifest -VersionContent $legacyVersionContent -InstallerContent $legacyInstallerContent -PackageIdentifier 'KhanhKit.PxGo' -Version '0.7.1' -ExpectedInstallers $legacyExpected | Out-Null
Assert-ThrowsLike {
    Assert-WingetInstallerManifest -VersionContent $legacyVersionContent -InstallerContent ($legacyInstallerContent.Replace('PortableCommandAlias: pxgo', 'PortableCommandAlias: wrong')) -PackageIdentifier 'KhanhKit.PxGo' -Version '0.7.1' -ExpectedInstallers $legacyExpected
} 'nested executable'
Write-Host 'PASS: legacy per-architecture nested installer layout and wrong-alias rejection'

$originalToken = [Environment]::GetEnvironmentVariable('WINGET_CREATE_GITHUB_TOKEN')
$originalRunnerTemp = [Environment]::GetEnvironmentVariable('RUNNER_TEMP')
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("pxgo-winget-retry-" + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempDir -Force
$env:WINGET_CREATE_GITHUB_TOKEN = 'mock-token-only'
$env:RUNNER_TEMP = $tempDir
$global:WingetMockMode = 'download-failure'
$global:WingetMockCalls = [System.Collections.Generic.List[string]]::new()

function Invoke-WebRequest {
    param($Uri, $OutFile, $Method, $MaximumRedirection, [switch]$UseBasicParsing)
    $global:WingetMockCalls.Add("web:" + [string]$Uri)
    if ($global:WingetMockMode -eq 'download-failure' -and $OutFile) {
        throw 'INJECTED_WIN_GET_CREATE_DOWNLOAD_FAILURE'
    }
    if ($OutFile) {
        [System.IO.File]::WriteAllText($OutFile, 'FAKE TEST BINARY; NEVER EXECUTED')
    }
    return [pscustomobject]@{ StatusCode = 200 }
}

function Get-FileHash {
    param($Path, $Algorithm)
    $global:WingetMockCalls.Add('file-hash')
    return [pscustomobject]@{ Hash = '24042bd37915805615e6cf969ac57c6439124c3fe85823327f5f3fb24bd9ffea' }
}

function Invoke-RestMethod {
    param($Uri, $Headers)
    $global:WingetMockCalls.Add("api:" + [string]$Uri)
    if ($Uri -like '*releases/tags/v0.9.0') {
        return $global:MockRelease
    }
    if ($Uri -like '*winget-pkgs/contents/*') {
        if ($global:WingetMockMode -in @('download-failure', 'pending-unverified')) {
            throw [System.Net.Http.HttpRequestException]::new('mock 404', $null, [System.Net.HttpStatusCode]::NotFound)
        }
        if ($Uri -like '*/KhanhKit.PxGo.yaml') {
            $content = $global:MockVersionContent
        }
        elseif ($Uri -like '*/KhanhKit.PxGo.installer.yaml') {
            if ($global:WingetMockMode -eq 'missing-installer') {
                throw [System.Net.Http.HttpRequestException]::new('mock 404', $null, [System.Net.HttpStatusCode]::NotFound)
            }
            $content = $global:MockInstallerContent
            if ($global:WingetMockMode -eq 'conflicting-installer') {
                $content = $content.Replace('E4001F1F', 'A4001F1F')
            }
        }
        else {
            throw "unexpected manifest API path: $Uri"
        }
        $rawBytes = [System.Text.Encoding]::UTF8.GetBytes($content)
        if ($global:WingetMockMode -eq 'invalid-utf8') {
            $rawBytes = [byte[]]@(0xff, 0xfe)
        }
        $fileType = 'file'
        if ($global:WingetMockMode -eq 'manifest-symlink') {
            $fileType = 'symlink'
        }
        return [pscustomobject]@{
            type = $fileType
            encoding = 'base64'
            truncated = ($global:WingetMockMode -eq 'truncated-manifest')
            size = $rawBytes.Length + [int]($global:WingetMockMode -eq 'incorrect-manifest-size')
            content = [Convert]::ToBase64String($rawBytes)
        }
    }
    if ($Uri -like 'https://api.github.com/search/issues*') {
        if ($global:WingetMockMode -eq 'pending-unverified') {
            return [pscustomobject]@{
                total_count = 1
                items = @([pscustomobject]@{ html_url = 'https://github.com/microsoft/winget-pkgs/pull/1' })
            }
        }
        return [pscustomobject]@{ total_count = 0; items = @() }
    }
    throw "Unexpected mocked API URL: $Uri"
}

try {
    Assert-ThrowsLike {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    } 'INJECTED_WIN_GET_CREATE_DOWNLOAD_FAILURE'
    if (-not ($global:WingetMockCalls -match 'wingetcreate.exe')) {
        throw 'Expected pinned publisher download on missing upstream version'
    }

    # Retry the SAME exact release tag and verify both upstream installers.
    $global:WingetMockCalls.Clear()
    $global:WingetMockMode = 'already-published'
    & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    if (-not ($global:WingetMockCalls -match 'KhanhKit.PxGo.installer.yaml')) {
        throw 'Expected upstream installer verification on retry'
    }
    if ($global:WingetMockCalls -match 'wingetcreate.exe') {
        throw 'Converged retry unexpectedly downloaded or executed publisher'
    }

    $global:WingetMockMode = 'conflicting-installer'
    Assert-ThrowsLike {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    } 'conflicts'
    $global:WingetMockMode = 'missing-installer'
    Assert-ThrowsLike {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    } 'lacks installer'
    foreach ($invalid in @(
        @{ Mode = 'truncated-manifest'; Error = 'unverified or truncated' },
        @{ Mode = 'manifest-symlink'; Error = 'unverified or truncated' },
        @{ Mode = 'incorrect-manifest-size'; Error = 'mismatched manifest size' },
        @{ Mode = 'invalid-utf8'; Error = 'invalid UTF-8' }
    )) {
        $global:WingetMockMode = $invalid.Mode
        Assert-ThrowsLike {
            & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
        } $invalid.Error
    }
    $global:WingetMockMode = 'pending-unverified'
    Assert-ThrowsLike {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    } 'pending manual SHA verification'
    Write-Host 'PASS: retry-only convergence plus truncated, symlink, malformed size, UTF-8, tampered/missing installer and pending PR all fail closed'
}
finally {
    [Environment]::SetEnvironmentVariable('WINGET_CREATE_GITHUB_TOKEN', $originalToken)
    [Environment]::SetEnvironmentVariable('RUNNER_TEMP', $originalRunnerTemp)
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Variable -Scope Global -Name WingetMockMode, WingetMockCalls, MockRelease, MockVersionContent, MockInstallerContent -ErrorAction SilentlyContinue
}
