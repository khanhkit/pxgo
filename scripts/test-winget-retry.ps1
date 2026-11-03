# Test WinGet-only retry behavior without network access or a real package
# publication. This script mocks transport commands; never supply real tokens.
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
        return [pscustomobject]@{
            draft = $false
            prerelease = $false
            published_at = '2026-10-07T01:03:00Z'
        }
    }
    if ($Uri -like '*winget-pkgs/contents/*') {
        # Simulate that another attempt has converged upstream.
        return [pscustomobject]@{ content = 'already-submitted' }
    }
    throw "Unexpected mocked API URL: $Uri"
}

try {
    $failedAsExpected = $false
    try {
        & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    }
    catch {
        if ($_.Exception.Message -like '*INJECTED_WIN_GET_CREATE_DOWNLOAD_FAILURE*') {
            $failedAsExpected = $true
        } else {
            throw
        }
    }
    if (-not $failedAsExpected) {
        throw 'Injected network failure did not fail the independent WinGet publisher'
    }

    # Retry with the *same* release identity. The already-published manifest
    # must be treated as converged without executing wingetcreate or any
    # release-promotion command.
    $global:WingetMockMode = 'already-published'
    & ./scripts/submit-winget-release.ps1 -Version '0.9.0' -Tag 'v0.9.0'
    if (-not ($global:WingetMockCalls -match 'winget-pkgs/contents/')) {
        throw 'Expected an upstream idempotency check on the retry'
    }
    if (-not (Test-Path (Join-Path $tempDir 'wingetcreate.exe'))) {
        throw 'Expected the pinned WingetCreate download path to be exercised'
    }
    Write-Host 'PASS: injected WinGet transient failure then retry-only convergence without release promotion'
}
finally {
    [Environment]::SetEnvironmentVariable('WINGET_CREATE_GITHUB_TOKEN', $originalToken)
    [Environment]::SetEnvironmentVariable('RUNNER_TEMP', $originalRunnerTemp)
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Variable -Scope Global -Name WingetMockMode, WingetMockCalls -ErrorAction SilentlyContinue
}
