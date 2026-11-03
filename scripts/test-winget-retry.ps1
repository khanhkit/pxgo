# Offline, Windows-native fault injection for the retry-only WinGet publisher.
# Mock data mirrors public v0.9.0 Release metadata, but never uses credentials.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$syncWorkflow = Get-Content -Raw '.github/workflows/winget-sync.yml'
if ($syncWorkflow -notmatch 'workflow_dispatch:' -or
    $syncWorkflow -match 'promote-release:|release-candidate:|goreleaser|gh release create' -or
    $syncWorkflow -notmatch 'git merge-base --is-ancestor' -or
    $syncWorkflow -notmatch 'submit-winget-release.ps1') {
    throw 'WinGet retry-only workflow contract is missing or contains release promotion'
}

$originalToken = [Environment]::GetEnvironmentVariable('WINGET_CREATE_GITHUB_TOKEN')
$originalRunnerTemp = [Environment]::GetEnvironmentVariable('RUNNER_TEMP')
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("pxgo-winget-retry-" + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempDir -Force
$env:WINGET_CREATE_GITHUB_TOKEN = 'mock-token-only'
$env:RUNNER_TEMP = $tempDir
$global:WingetMockMode = 'download-failure'
$global:WingetMockCalls = [System.Collections.Generic.List[string]]::new()
$global:WingetPassCount = 0

$x64Digest = 'e4001f1fc62e26472ac96fae5d5c3dcee5bd23789e671d2df0884b6070c8261f'
$arm64Digest = '480e692ec7868df13cd159632b726d80a4d4e212056c6864d61a56617f55e38b'
$x64Url = 'https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_amd64.zip'
$arm64Url = 'https://github.com/khanhkit/pxgo/releases/download/v0.9.0/pxgo_windows_arm64.zip'
$versionYaml = @'
PackageIdentifier: KhanhKit.PxGo
PackageVersion: 0.9.0
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.12.0
'@
$installerYaml = @"
PackageIdentifier: KhanhKit.PxGo
PackageVersion: 0.9.0
InstallerType: zip
Installers:
- Architecture: x64
  InstallerUrl: $x64Url
  InstallerSha256: $x64Digest
- Architecture: arm64
  InstallerUrl: $arm64Url
  InstallerSha256: $arm64Digest
ManifestType: installer
ManifestVersion: 1.12.0
"@

function Invoke-WebRequest {
    param($Uri, $OutFile, $Method, $MaximumRedirection, [switch]$UseBasicParsing)
    $global:WingetMockCalls.Add("web:" + [string]$Uri)
    if ($global:WingetMockMode -eq 'download-failure') {
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
        $assets = @(
            [pscustomobject]@{ name = 'pxgo_windows_amd64.zip'; digest = "sha256:$x64Digest"; browser_download_url = $x64Url; size = 7888477 },
            [pscustomobject]@{ name = 'pxgo_windows_arm64.zip'; digest = "sha256:$arm64Digest"; browser_download_url = $arm64Url; size = 7115281 }
        )
        if ($global:WingetMockMode -eq 'missing-release-digest') { $assets[0].digest = $null }
        if ($global:WingetMockMode -eq 'wrong-release-url') { $assets[0].browser_download_url = 'https://example.com/evil.zip' }
        if ($global:WingetMockMode -eq 'duplicate-release-asset') { $assets += $assets[0] }
        $tagName = if ($global:WingetMockMode -eq 'wrong-release-tag') { 'v0.9.1' } else { 'v0.9.0' }
        return [pscustomobject]@{
            tag_name = $tagName
            draft = $false
            prerelease = $false
            published_at = '2026-10-07T01:03:00Z'
            assets = $assets
        }
    }
    if ($Uri -like '*winget-pkgs/contents/*') {
        if ($global:WingetMockMode -in @('not-published-with-pending', 'not-published-without-pending')) {
            throw [System.Net.Http.HttpRequestException]::new('mock 404', $null, [System.Net.HttpStatusCode]::NotFound)
        }
        $yaml = if ($Uri -like '*.installer.yaml') { $installerYaml } else { $versionYaml }
        switch ($global:WingetMockMode) {
            'wrong-upstream-sha' { if ($Uri -like '*.installer.yaml') { $yaml = $yaml.Replace($x64Digest, ('a' * 64)) } }
            'wrong-upstream-url' { if ($Uri -like '*.installer.yaml') { $yaml = $yaml.Replace($x64Url, 'https://example.com/evil.zip') } }
            'wrong-upstream-version' { if ($Uri -like '*KhanhKit.PxGo.yaml') { $yaml = $yaml.Replace('PackageVersion: 0.9.0', 'PackageVersion: 0.9.1') } }
            'duplicate-installer' { if ($Uri -like '*.installer.yaml') { $yaml += "`n- Architecture: x64`n  InstallerUrl: $x64Url`n  InstallerSha256: $x64Digest`n" } }
            'duplicate-package-identity' { if ($Uri -like '*.installer.yaml') { $yaml += "`nPackageIdentifier: Malicious.Proxy`n" } }
            'truncated-manifest' { return [pscustomobject]@{ content = ''; encoding = 'base64'; truncated = $true } }
        }
        return [pscustomobject]@{
            content = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($yaml))
            encoding = 'base64'
            truncated = $false
        }
    }
    if ($Uri -like '*api.github.com/search/issues*') {
        if ($global:WingetMockMode -eq 'not-published-with-pending') {
            return [pscustomobject]@{ total_count = 1; items = @([pscustomobject]@{ html_url = 'https://github.com/microsoft/winget-pkgs/pull/999999' }) }
        }
        throw 'Mock intentionally does not permit executing an unsigned wingetcreate.exe'
    }
    throw "Unexpected mocked API URL: $Uri"
}

function Assert-PublisherFailure {
    param([string]$Mode, [string]$ExpectedMessage)
    $global:WingetMockMode = $Mode
    $global:WingetMockCalls.Clear()
    $failed = $false
    try {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    }
    catch {
        if ($_.Exception.Message -like "*$ExpectedMessage*") { $failed = $true }
        else { throw "Unexpected failure for $Mode : $($_.Exception.Message)" }
    }
    if (-not $failed) { throw "WinGet $Mode unexpectedly reported success" }
    if (@($global:WingetMockCalls | Where-Object { $_ -match 'releases?/create|promote-release|gh release' }).Count -ne 0) {
        throw "WinGet $Mode attempted immutable release promotion"
    }
    $global:WingetPassCount++
    Write-Host "PASS: $Mode fails closed"
}

try {
    Assert-PublisherFailure 'download-failure' 'INJECTED_WIN_GET_CREATE_DOWNLOAD_FAILURE'

    $global:WingetMockMode = 'already-published'
    $global:WingetMockCalls.Clear()
    & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    if (@($global:WingetMockCalls | Where-Object { $_ -like '*KhanhKit.PxGo.installer.yaml' }).Count -ne 1 -or
        -not (Test-Path (Join-Path $tempDir 'wingetcreate.exe'))) {
        throw 'Exact upstream idempotency proof did not read both manifests'
    }
    $global:WingetPassCount++
    Write-Host 'PASS: exact upstream merged WinGet URLs and SHA256 converge without release promotion'

    Assert-PublisherFailure 'wrong-upstream-sha' 'checksum conflicts'
    Assert-PublisherFailure 'wrong-upstream-url' 'URL or digest fields'
    Assert-PublisherFailure 'wrong-upstream-version' 'package identity/version/type differs'
    Assert-PublisherFailure 'duplicate-installer' 'exactly two explicit installers'
    Assert-PublisherFailure 'duplicate-package-identity' 'missing or ambiguous'
    Assert-PublisherFailure 'truncated-manifest' 'unverifiable upstream'
    Assert-PublisherFailure 'missing-release-digest' 'missing or untrusted SHA256 identity'
    Assert-PublisherFailure 'wrong-release-url' 'missing or untrusted SHA256 identity'
    Assert-PublisherFailure 'duplicate-release-asset' 'missing or untrusted SHA256 identity'
    Assert-PublisherFailure 'wrong-release-tag' 'requires a stable GitHub Release'
    Assert-PublisherFailure 'not-published-with-pending' 'pending but content is not yet verified'

    Write-Host "PASS: $global:WingetPassCount WinGet release provenance / fault-injection scenarios"
}
finally {
    [Environment]::SetEnvironmentVariable('WINGET_CREATE_GITHUB_TOKEN', $originalToken)
    [Environment]::SetEnvironmentVariable('RUNNER_TEMP', $originalRunnerTemp)
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Variable -Scope Global -Name WingetMockMode, WingetMockCalls, WingetPassCount -ErrorAction SilentlyContinue
}
