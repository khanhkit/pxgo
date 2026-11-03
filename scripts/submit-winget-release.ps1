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

$apiHeaders = @{
    'Authorization' = "Bearer $($env:WINGET_CREATE_GITHUB_TOKEN)"
    'User-Agent' = 'PxGo-WinGet-Publisher'
    'Accept' = 'application/vnd.github+json'
}
$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases/tags/$Tag" -Headers $apiHeaders
if ($release.tag_name -ne $Tag -or $release.draft -or $release.prerelease) {
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

# A published manifest is converged only when BOTH architectures match the
# immutable GitHub Release assets, not merely when a version YAML file exists.
# Read metadata through the GitHub contents API; fail closed on unparseable YAML.
function Get-UpstreamManifestText {
    param([string]$Uri)
    $response = Invoke-RestMethod -Uri $Uri -Headers $apiHeaders
    if ($null -eq $response -or $response.encoding -ne 'base64' -or
        $response.truncated -eq $true -or [string]::IsNullOrWhiteSpace($response.content)) {
        throw "unverifiable upstream WinGet manifest content: $Uri"
    }
    try {
        return [Text.Encoding]::UTF8.GetString(
            [Convert]::FromBase64String(($response.content -replace '\s', ''))
        )
    }
    catch {
        throw "invalid base64 upstream WinGet manifest content: $Uri"
    }
}

function Get-UniqueManifestScalar {
    param([string]$Text, [string]$Name)
    # Only accept the unquoted flat syntax emitted by pinned WingetCreate.
    # Missing, duplicate, quoted, or complex YAML fields are not proof.
    $pattern = '(?m)^' + [regex]::Escape($Name) + ':[ \t]*([^\r\n# \t]+)[ \t]*\r?$'
    $found = [regex]::Matches($Text, $pattern)
    if ($found.Count -ne 1) {
        throw "missing or ambiguous WinGet $Name (matches=$($found.Count), length=$($Text.Length), firstCode=$([int]$Text[0]), pattern=$pattern, prefix=$($Text.Substring(0, [Math]::Min(42, $Text.Length))))"
    }
    return $found[0].Groups[1].Value
}

function Assert-UpstreamExactRelease {
    param([string]$VersionText, [string]$InstallerText)
    foreach ($manifest in @(
        @{ Text = $VersionText; Type = 'version' },
        @{ Text = $InstallerText; Type = 'installer' }
    )) {
        if ((Get-UniqueManifestScalar $manifest.Text 'PackageIdentifier') -cne $PackageIdentifier -or
            (Get-UniqueManifestScalar $manifest.Text 'PackageVersion') -cne $Version -or
            (Get-UniqueManifestScalar $manifest.Text 'ManifestType') -cne $manifest.Type) {
            throw "WinGet upstream package identity/version/type differs from release $Tag"
        }
    }
    if ($InstallerText -notmatch '(?m)^Installers:[ \t]*\r?$' -or
        $InstallerText -match '(?m)^[ \t]*<<:') {
        throw 'upstream WinGet installer manifest structure is unsupported'
    }

    $installers = [regex]::Matches($InstallerText, '(?m)^-[ \t]*Architecture:[ \t]*([^\r\n \t]+)[ \t]*\r?$')
    if ($installers.Count -ne 2) {
        throw 'upstream WinGet must contain exactly two explicit installers'
    }
    $expectedAssets = @{
        'x64' = @{ Name = 'pxgo_windows_amd64.zip'; Url = $x64Url }
        'arm64' = @{ Name = 'pxgo_windows_arm64.zip'; Url = $arm64Url }
    }
    $seen = @{}
    for ($i = 0; $i -lt $installers.Count; $i++) {
        $architecture = $installers[$i].Groups[1].Value
        if (-not $expectedAssets.ContainsKey($architecture) -or $seen.ContainsKey($architecture)) {
            throw "unexpected or duplicate WinGet installer architecture: $architecture"
        }
        $seen[$architecture] = $true
        $start = $installers[$i].Index
        $end = if ($i + 1 -lt $installers.Count) { $installers[$i + 1].Index } else { $InstallerText.Length }
        $section = $InstallerText.Substring($start, $end - $start)
        $url = [regex]::Matches($section, '(?m)^[ \t]+InstallerUrl:[ \t]*([^\r\n \t#]+)[ \t]*\r?$')
        $digest = [regex]::Matches($section, '(?m)^[ \t]+InstallerSha256:[ \t]*([0-9a-fA-F]{64})[ \t]*\r?$')
        if ($url.Count -ne 1 -or $digest.Count -ne 1 -or
            $url[0].Groups[1].Value -cne $expectedAssets[$architecture].Url) {
            throw "WinGet upstream URL or digest fields do not match release: $architecture"
        }
        $assetMatches = @($release.assets | Where-Object { $_.name -ceq $expectedAssets[$architecture].Name })
        if ($assetMatches.Count -ne 1 -or
            $assetMatches[0].digest -cnotmatch '^sha256:[0-9a-f]{64}$' -or
            $assetMatches[0].browser_download_url -cne $expectedAssets[$architecture].Url -or
            $assetMatches[0].size -le 0) {
            throw "GitHub Release asset has missing or untrusted SHA256 identity: $architecture"
        }
        $releaseDigest = $assetMatches[0].digest.Substring(7)
        if ($digest[0].Groups[1].Value.ToLowerInvariant() -cne $releaseDigest) {
            throw "WinGet upstream installer checksum conflicts with GitHub Release: $architecture"
        }
    }
}

# Idempotency: when both merged manifests and the immutable release metadata
# prove matching identity, retry may return without re-submitting.
$manifestPath = "manifests/k/KhanhKit/PxGo/$Version/KhanhKit.PxGo.yaml"
$encodedPath = [System.Uri]::EscapeDataString($manifestPath).Replace('%2F', '/')
$manifestApi = "https://api.github.com/repos/microsoft/winget-pkgs/contents/$encodedPath"
$versionManifest = $null
try {
    $versionManifest = Get-UpstreamManifestText $manifestApi
}
catch {
    # Only genuine 404 allows a new submission. Parse failures and all other
    # HTTP failures MUST block rather than permit an overwrite or false green.
    $status = $null
    if ($null -ne $_.Exception.PSObject.Properties['Response'] -and $null -ne $_.Exception.Response) {
        $status = [int]$_.Exception.Response.StatusCode
    }
    elseif ($null -ne $_.Exception.PSObject.Properties['StatusCode'] -and $null -ne $_.Exception.StatusCode) {
        $status = [int]$_.Exception.StatusCode
    }
    if ($status -ne 404) { throw }
}
if ($null -ne $versionManifest) {
    $installerApi = $manifestApi.Replace('/KhanhKit.PxGo.yaml', '/KhanhKit.PxGo.installer.yaml')
    $installerManifest = Get-UpstreamManifestText $installerApi
    Assert-UpstreamExactRelease $versionManifest $installerManifest
    Write-Host "Verified WinGet $PackageIdentifier $Version merged upstream: URLs and SHA256 match GitHub Release."
    return
}

# Avoid duplicate PRs on workflow re-runs while Microsoft moderation is pending.
$searchQuery = [System.Uri]::EscapeDataString("repo:microsoft/winget-pkgs is:pr is:open `"$PackageIdentifier`" `"$Version`"")
$pending = Invoke-RestMethod -Uri "https://api.github.com/search/issues?q=$searchQuery" -Headers $apiHeaders
if ($pending.total_count -gt 0) {
    $urls = @($pending.items | ForEach-Object { $_.html_url }) -join ', '
    throw "WinGet submission pending but content is not yet verified upstream for $PackageIdentifier ${Version}: $urls"
}

# Only download/execute WingetCreate when a new submission is genuinely needed.
# A converged retry must work even if the WingetCreate CDN is temporarily down.
$wingetCreateVersion = '1.12.13.0'
$wingetCreateSha256 = '24042bd37915805615e6cf969ac57c6439124c3fe85823327f5f3fb24bd9ffea'
$wingetCreateUrl = "https://github.com/microsoft/winget-create/releases/download/v$wingetCreateVersion/wingetcreate.exe"
$wingetCreatePath = Join-Path $env:RUNNER_TEMP 'wingetcreate.exe'

Invoke-WebRequest -Uri $wingetCreateUrl -OutFile $wingetCreatePath -UseBasicParsing
$actualHash = (Get-FileHash -Path $wingetCreatePath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -ne $wingetCreateSha256) {
    throw "WingetCreate checksum mismatch: expected=$wingetCreateSha256 actual=$actualHash"
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
