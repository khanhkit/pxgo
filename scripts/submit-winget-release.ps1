[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$')]
    [string]$Version,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$')]
    [string]$Tag,

    [string]$Repository = 'khanhkit/pxgo',
    [string]$PackageIdentifier = 'KhanhKit.PxGo'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ($Tag -ne "v$Version") {
    throw "tag/version mismatch: tag=$Tag version=$Version"
}

if ([string]::IsNullOrWhiteSpace($env:WINGET_CREATE_GITHUB_TOKEN)) {
    throw 'WINGET_CREATE_GITHUB_TOKEN is required for WinGet submission'
}

$wingetCreateVersion = '1.12.13.0'
$wingetCreateSha256 = '24042bd37915805615e6cf969ac57c6439124c3fe85823327f5f3fb24bd9ffea'
$wingetCreateUrl = "https://github.com/microsoft/winget-create/releases/download/v$wingetCreateVersion/wingetcreate.exe"
$wingetCreatePath = Join-Path $env:RUNNER_TEMP 'wingetcreate.exe'

Invoke-WebRequest -Uri $wingetCreateUrl -OutFile $wingetCreatePath -UseBasicParsing
$actualHash = (Get-FileHash -Path $wingetCreatePath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -ne $wingetCreateSha256) {
    throw "WingetCreate checksum mismatch: expected=$wingetCreateSha256 actual=$actualHash"
}

$apiHeaders = @{
    'Authorization' = "Bearer $($env:WINGET_CREATE_GITHUB_TOKEN)"
    'User-Agent' = 'PxGo-WinGet-Publisher'
    'Accept' = 'application/vnd.github+json'
}
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases/tags/$Tag" -Headers $apiHeaders
if ($release.draft -or $release.prerelease) {
    throw "WinGet publication requires a stable GitHub Release: $Tag"
}
$releaseDate = ([DateTimeOffset]$release.published_at).UtcDateTime.ToString('yyyy-MM-dd')

$baseUrl = "https://github.com/$Repository/releases/download/$Tag"
$x64Url = "$baseUrl/pxgo_windows_amd64.zip"
$arm64Url = "$baseUrl/pxgo_windows_arm64.zip"
$releaseUrl = "https://github.com/$Repository/releases/tag/$Tag"

foreach ($url in @($x64Url, $arm64Url)) {
    $response = Invoke-WebRequest -Uri $url -Method Head -MaximumRedirection 5 -UseBasicParsing
    if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 400) {
        throw "release installer is not publicly available: $url status=$($response.StatusCode)"
    }
}

# Idempotency: once the exact version is merged upstream, a workflow rerun must
# not create another submission.
$manifestPath = "manifests/k/KhanhKit/PxGo/$Version/KhanhKit.PxGo.yaml"
$encodedPath = [System.Uri]::EscapeDataString($manifestPath).Replace('%2F', '/')
$manifestApi = "https://api.github.com/repos/microsoft/winget-pkgs/contents/$encodedPath"
try {
    Invoke-RestMethod -Uri $manifestApi -Headers $apiHeaders | Out-Null
    Write-Host "WinGet $PackageIdentifier $Version already exists upstream; submission is converged."
    exit 0
}
catch {
    $statusCode = [int]$_.Exception.Response.StatusCode
    if ($statusCode -ne 404) {
        throw
    }
}

# Avoid duplicate PRs on workflow re-runs while Microsoft moderation is pending.
$searchQuery = [System.Uri]::EscapeDataString("repo:microsoft/winget-pkgs is:pr is:open `"$PackageIdentifier`" `"$Version`"")
$pending = Invoke-RestMethod -Uri "https://api.github.com/search/issues?q=$searchQuery" -Headers $apiHeaders
if ($pending.total_count -gt 0) {
    $urls = @($pending.items | ForEach-Object { $_.html_url }) -join ', '
    Write-Host "WinGet submission already pending for $PackageIdentifier $Version: $urls"
    exit 0
}

$prTitle = "New version: $PackageIdentifier version $Version"
$arguments = @(
    'update',
    $PackageIdentifier,
    '--submit',
    '--no-open',
    '--version', $Version,
    '--urls', "$x64Url|x64", "$arm64Url|arm64",
    '--release-date', $releaseDate,
    '--release-notes-url', $releaseUrl,
    '--prtitle', $prTitle
)

# WingetCreate consumes WINGET_CREATE_GITHUB_TOKEN directly. Do not pass the
# token as a command-line argument; command lines can be exposed in CI logs.
& $wingetCreatePath @arguments
if ($LASTEXITCODE -ne 0) {
    throw "WingetCreate submission failed with exit code $LASTEXITCODE"
}

Write-Host "Submitted WinGet update for $PackageIdentifier $Version"
