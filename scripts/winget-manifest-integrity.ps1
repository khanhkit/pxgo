# Pure, network-free validation helpers used by the WinGet publisher and its tests.
# The manifest parser accepts the small, explicit subset generated for PxGo
# portable ZIP installers; unfamiliar YAML shapes fail closed.
function Get-ExpectedWingetInstallers {
    param(
        [Parameter(Mandatory = $true)]$Release,
        [Parameter(Mandatory = $true)][string]$Tag,
        [string]$Repository = 'khanhkit/pxgo'
    )

    if ($Release.tag_name -cne $Tag -or $Release.draft -or $Release.prerelease -or
        [string]::IsNullOrWhiteSpace([string]$Release.published_at)) {
        throw "release metadata does not identify a published stable release for $Tag"
    }

    $result = @{}
    foreach ($spec in @(
        @{ Architecture = 'x64'; Filename = 'pxgo_windows_amd64.zip' },
        @{ Architecture = 'arm64'; Filename = 'pxgo_windows_arm64.zip' }
    )) {
        $filename = $spec.Filename
        $url = "https://github.com/$Repository/releases/download/$Tag/$filename"
        $assets = @($Release.assets | Where-Object { $_.name -ceq $filename })
        if ($assets.Count -ne 1) {
            throw "release $Tag must contain exactly one $filename artifact"
        }
        $asset = $assets[0]
        $digest = [string]$asset.digest
        $digestMatch = [regex]::Match($digest, '^sha256:([0-9a-fA-F]{64})$')
        if (-not $digestMatch.Success -or
            $asset.browser_download_url -cne $url -or
            [int64]$asset.size -le 0 -or
            ($asset.state -and $asset.state -cne 'uploaded')) {
            throw "release $Tag artifact $filename lacks a trustworthy URL/size/SHA256 digest"
        }
        $result[$spec.Architecture] = @{
            Url = $url
            Sha256 = $digestMatch.Groups[1].Value.ToLowerInvariant()
        }
    }
    return $result
}

function Get-WingetManifestMetadata {
    param([Parameter(Mandatory = $true)][string]$Content)

    $result = @{}
    foreach ($line in ($Content -split '\r?\n')) {
        if ($line -cmatch '^([a-zA-Z][a-zA-Z0-9]*):[ \t]*([^#\r\n]*?)[ \t]*$') {
            $key = $Matches[1]
            if ($result.ContainsKey($key)) {
                throw "duplicate WinGet manifest root field: $key"
            }
            $result[$key] = $Matches[2]
        }
    }
    return $result
}

function Assert-WingetInstallerManifest {
    param(
        [Parameter(Mandatory = $true)][string]$VersionContent,
        [Parameter(Mandatory = $true)][string]$InstallerContent,
        [Parameter(Mandatory = $true)][string]$PackageIdentifier,
        [Parameter(Mandatory = $true)][string]$Version,
        [Parameter(Mandatory = $true)][hashtable]$ExpectedInstallers
    )

    $versionFields = Get-WingetManifestMetadata -Content $VersionContent
    $installerFields = Get-WingetManifestMetadata -Content $InstallerContent
    foreach ($entry in @(
        @{ Name = 'version'; Fields = $versionFields; Type = 'version' },
        @{ Name = 'installer'; Fields = $installerFields; Type = 'installer' }
    )) {
        $fields = $entry.Fields
        if ($fields['PackageIdentifier'] -cne $PackageIdentifier -or
            $fields['PackageVersion'] -cne $Version -or
            $fields['ManifestType'] -cne $entry.Type) {
            throw "WinGet $($entry.Name) manifest identity/version/type does not match $PackageIdentifier $Version"
        }
    }

    # Matching archive digests alone is insufficient if the manifest silently
    # selects a different nested executable or installer type.
    if ($installerFields['InstallerType'] -cne 'zip' -or
        $installerFields['NestedInstallerType'] -cne 'portable') {
        throw 'WinGet installer packaging semantics conflict with PxGo ZIP portable policy'
    }
    $nestedBlock = [regex]::Match(
        $InstallerContent,
        '(?ms)^NestedInstallerFiles:[ \t]*\r?\n(?<body>.*?)(?=^[A-Za-z][A-Za-z0-9]*:|\z)'
    )
    # WinGet 0.7.1 used per-architecture nested files; subsequent releases
    # use a single top-level nested file. Both are valid for identical ZIPs.
    $globalNested = $nestedBlock.Success
    if ($globalNested) {
        $nestedLines = @($nestedBlock.Groups['body'].Value -split '\r?\n' |
            Where-Object { $_ -notmatch '^[ \t]*(#|$)' })
        if ($nestedLines.Count -ne 2 -or
            $nestedLines[0] -cnotmatch '^-[ \t]+RelativeFilePath:[ \t]*pxgo\.exe[ \t]*$' -or
            $nestedLines[1] -cnotmatch '^[ \t]+PortableCommandAlias:[ \t]*pxgo[ \t]*$') {
            throw 'WinGet nested executable or portable command alias conflicts with PxGo'
        }
    }

    $entries = @{}
    $inside = $false
    $current = $null
    foreach ($line in ($InstallerContent -split '\r?\n')) {
        if ($line -match '^[ \t]*(#|$)') {
            continue
        }
        if ($line -cmatch '^Installers:[ \t]*$') {
            if ($inside) {
                throw 'duplicate Installers section'
            }
            $inside = $true
            continue
        }
        if (-not $inside) {
            continue
        }
        if ($line -cmatch '^[A-Za-z][A-Za-z0-9]*:') {
            $inside = $false
            $current = $null
            continue
        }
        if ($line -cmatch '^-[ \t]+Architecture:[ \t]*([^ \t]+)[ \t]*$') {
            $arch = $Matches[1]
            if (-not $ExpectedInstallers.ContainsKey($arch) -or $entries.ContainsKey($arch)) {
                throw "unexpected or duplicate WinGet installer architecture $arch"
            }
            $current = @{}
            $entries[$arch] = $current
            continue
        }
        if ($null -eq $current) {
            throw "WinGet installer field without architecture: $line"
        }
        if ($line -cmatch '^[ \t]+-[ \t]+RelativeFilePath:[ \t]*(.*?)[ \t]*$') {
            $key = 'RelativeFilePath'
            $value = $Matches[1]
        }
        elseif ($line -cmatch '^[ \t]+([A-Za-z][A-Za-z0-9]*):[ \t]*(.*?)[ \t]*$') {
            $key = $Matches[1]
            $value = $Matches[2]
        } else {
            throw "unrecognized WinGet installer entry: $line"
        }
        if ($current.ContainsKey($key)) {
            throw "duplicate WinGet installer field $key"
        }
        $current[$key] = $value
    }

    if ($entries.Count -ne $ExpectedInstallers.Count) {
        throw "WinGet installer architecture count $($entries.Count) does not match release"
    }
    foreach ($arch in $ExpectedInstallers.Keys) {
        $actual = $entries[$arch]
        $expected = $ExpectedInstallers[$arch]
        if ($null -eq $actual) {
            throw "WinGet $arch installer is missing"
        }
        if ($globalNested) {
            if ($actual.Count -ne 2) {
                throw "WinGet $arch has unexpected per-architecture installer fields"
            }
        } elseif ($actual.Count -ne 5 -or
                  $actual['NestedInstallerFiles'] -cne '' -or
                  $actual['RelativeFilePath'] -cne 'pxgo.exe' -or
                  $actual['PortableCommandAlias'] -cne 'pxgo') {
            throw "WinGet $arch nested executable or portable command alias conflicts with PxGo"
        }
        if ($actual['InstallerUrl'] -cne $expected.Url -or
            $actual['InstallerSha256'] -cnotmatch '^[0-9a-fA-F]{64}$' -or
            $actual['InstallerSha256'].ToLowerInvariant() -cne $expected.Sha256) {
            throw "WinGet $arch URL/SHA256 conflicts with immutable GitHub Release assets"
        }
    }
    return $true
}

function Get-OptionalGitHubManifest {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][hashtable]$Headers
    )

    try {
        $response = Invoke-RestMethod -Uri $Uri -Headers $Headers
    }
    catch {
        $statusCode = $null
        if ($_.Exception.PSObject.Properties['Response'] -and
            $null -ne $_.Exception.Response -and
            $_.Exception.Response.PSObject.Properties['StatusCode']) {
            $statusCode = [int]$_.Exception.Response.StatusCode
        }
        elseif ($_.Exception.PSObject.Properties['StatusCode'] -and
                $null -ne $_.Exception.StatusCode) {
            $statusCode = [int]$_.Exception.StatusCode
        }
        if ($statusCode -eq 404) {
            return $null
        }
        throw
    }

    if ($response.encoding -cne 'base64' -or [string]::IsNullOrWhiteSpace([string]$response.content)) {
        throw "GitHub API response for $Uri lacks base64 file content"
    }
    try {
        $raw = [Convert]::FromBase64String(([string]$response.content -replace '\s', ''))
        return [System.Text.Encoding]::UTF8.GetString($raw)
    }
    catch {
        throw "GitHub API response for $Uri contains invalid base64 data"
    }
}
