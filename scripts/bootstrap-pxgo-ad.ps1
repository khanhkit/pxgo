#requires -RunAsAdministrator
#requires -Version 5.1
<#
.SYNOPSIS
  Builds the protected PxGo real-AD verification fixture on one disposable Windows Server x64 VM.

.DESCRIPTION
  Stage Promote installs AD DS/DNS and creates a new forest. A reboot is required.
  Stage Configure creates the GitHub runner service identity, DNS alias and HTTP SPN,
  then installs the pinned GitHub Actions runner as that domain identity.
  Stage Validate verifies AD/DNS/SPN/runner prerequisites without changing them.

  This lab is intentionally single-VM and disposable. The domain controller also hosts
  the self-hosted GitHub runner and the in-process Negotiate acceptor used by
  TestTCSSPIWINAD010RealADNegotiateUsesKerberos.

  Never expose this fixture as a general-purpose runner. The pxgo-ad workflow is gated
  behind workflow_dispatch + a protected GitHub environment.

.EXAMPLE
  # First boot, elevated PowerShell:
  .\scripts\bootstrap-pxgo-ad.ps1 -Stage Promote -RebootAfterPromote

  # After reboot, log in as the forest Administrator. Obtain a one-time runner token:
  # gh api --method POST repos/khanhkit/pxgo/actions/runners/registration-token --jq .token
  $env:PXGO_RUNNER_TOKEN = '<one-time-token>'
  .\scripts\bootstrap-pxgo-ad.ps1 -Stage Configure

  # Verify:
  .\scripts\bootstrap-pxgo-ad.ps1 -Stage Validate
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Promote', 'Configure', 'Validate')]
    [string]$Stage,

    [ValidatePattern('^[A-Za-z0-9.-]+$')]
    [string]$DomainName = 'pxgo.test',

    [ValidatePattern('^[A-Za-z0-9-]+$')]
    [string]$DomainNetbiosName = 'PXGO',

    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$RunnerAccount = 'pxgo-runner',

    [ValidatePattern('^[A-Za-z0-9.-]+$')]
    [string]$ProxyHost = 'proxy.pxgo.test',

    [string]$RepositoryUrl = 'https://github.com/khanhkit/pxgo',

    [string]$RunnerVersion = '2.337.0',

    [string]$RunnerSha256 = '1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc',

    [string]$RunnerDirectory = 'C:\actions-runner',

    [string]$RunnerName = "$env:COMPUTERNAME-pxgo-ad",

    [string]$RunnerToken = $env:PXGO_RUNNER_TOKEN,

    [SecureString]$SafeModeAdministratorPassword,

    [SecureString]$RunnerAccountPassword,

    [switch]$RebootAfterPromote
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'bootstrap-pxgo-ad.ps1 must run from an elevated PowerShell session'
    }
}

function New-LabPassword {
    $bytes = New-Object byte[] 30
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($bytes)
    }
    finally {
        $rng.Dispose()
    }
    return ([Convert]::ToBase64String($bytes) + 'aA1!')
}

function ConvertTo-PlainText {
    param([Parameter(Mandatory = $true)][SecureString]$Value)
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
    }
}

function Get-PrimaryIPv4 {
    $route = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' |
        Sort-Object RouteMetric, InterfaceMetric |
        Select-Object -First 1
    if (-not $route) {
        throw 'no IPv4 default route found; configure the VM network before building the AD fixture'
    }

    $address = Get-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $route.InterfaceIndex |
        Where-Object {
            $_.IPAddress -ne '127.0.0.1' -and
            $_.AddressState -eq 'Preferred'
        } |
        Select-Object -First 1
    if (-not $address) {
        throw "no preferred IPv4 address found on interface $($route.InterfaceIndex)"
    }
    return $address.IPAddress
}

function Assert-ProxyHostInDomain {
    $suffix = '.' + $DomainName.TrimStart('.')
    if (-not $ProxyHost.EndsWith($suffix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "ProxyHost $ProxyHost must be inside DomainName $DomainName"
    }
}

function Get-ProxyRecordName {
    Assert-ProxyHostInDomain
    $suffixLength = $DomainName.Length + 1
    return $ProxyHost.Substring(0, $ProxyHost.Length - $suffixLength)
}

function Ensure-ADForest {
    Assert-Administrator

    $os = Get-CimInstance Win32_OperatingSystem
    if ($os.ProductType -eq 1) {
        throw 'Stage Promote requires Windows Server, not a client Windows edition'
    }

    $computer = Get-CimInstance Win32_ComputerSystem
    if ($computer.DomainRole -ge 4) {
        Write-Host "Machine is already a domain controller for $($computer.Domain); skipping promotion."
        return
    }

    $feature = Get-WindowsFeature AD-Domain-Services
    if (-not $feature.Installed) {
        Install-WindowsFeature AD-Domain-Services -IncludeManagementTools | Out-Null
    }

    Import-Module ADDSDeployment
    $dsrm = $SafeModeAdministratorPassword
    if (-not $dsrm) {
        $dsrm = ConvertTo-SecureString (New-LabPassword) -AsPlainText -Force
        Write-Warning 'Generated an ephemeral DSRM password for this disposable lab. Rebuild the VM rather than treating it as a recoverable production domain.'
    }

    $forestArgs = @{
        DomainName = $DomainName
        DomainNetbiosName = $DomainNetbiosName
        InstallDNS = $true
        SafeModeAdministratorPassword = $dsrm
        NoRebootOnCompletion = $true
        Force = $true
    }
    Install-ADDSForest @forestArgs | Out-Null

    Write-Host "AD forest $DomainName created. A reboot is required before Stage Configure."
    if ($RebootAfterPromote) {
        Restart-Computer -Force
    }
}

function Ensure-DomainReady {
    Assert-Administrator
    Import-Module ActiveDirectory
    Import-Module DnsServer

    $computer = Get-CimInstance Win32_ComputerSystem
    if ($computer.DomainRole -lt 4) {
        throw 'machine is not a domain controller; run Stage Promote and reboot first'
    }
    if (-not $computer.Domain.Equals($DomainName, [StringComparison]::OrdinalIgnoreCase)) {
        throw "machine domain $($computer.Domain) does not match expected $DomainName"
    }
}

function Ensure-RunnerIdentity {
    Ensure-DomainReady

    $existing = Get-ADUser -Filter "SamAccountName -eq '$RunnerAccount'" -ErrorAction SilentlyContinue
    $runnerService = Get-CimInstance Win32_Service |
        Where-Object { $_.Name -like 'actions.runner.*' } |
        Select-Object -First 1

    $securePassword = $RunnerAccountPassword
    if (-not $runnerService) {
        if (-not $securePassword) {
            $securePassword = ConvertTo-SecureString (New-LabPassword) -AsPlainText -Force
        }

        if (-not $existing) {
            $userArgs = @{
                Name = $RunnerAccount
                SamAccountName = $RunnerAccount
                UserPrincipalName = "$RunnerAccount@$DomainName"
                AccountPassword = $securePassword
                Enabled = $true
                PasswordNeverExpires = $true
                CannotChangePassword = $true
            }
            New-ADUser @userArgs
        }
        else {
            Set-ADAccountPassword -Identity $existing -Reset -NewPassword $securePassword
            Enable-ADAccount -Identity $existing
            Set-ADUser -Identity $existing -PasswordNeverExpires $true
        }
    }
    elseif (-not $existing) {
        throw "GitHub runner service exists, but AD account $RunnerAccount does not"
    }

    return @{
        User = (Get-ADUser -Identity $RunnerAccount -Properties UserPrincipalName, ServicePrincipalName)
        Password = $securePassword
        ExistingService = $runnerService
    }
}

function Ensure-ProxyDNSAndSPN {
    param([Parameter(Mandatory = $true)]$RunnerIdentity)

    $recordName = Get-ProxyRecordName
    $ip = Get-PrimaryIPv4

    $existingRecord = Get-DnsServerResourceRecord -ZoneName $DomainName -Name $recordName -RRType A -ErrorAction SilentlyContinue

    $hasExpected = $false
    foreach ($record in @($existingRecord)) {
        if ($record.RecordData.IPv4Address.IPAddressToString -eq $ip) {
            $hasExpected = $true
        }
    }
    if (-not $hasExpected) {
        if ($existingRecord) {
            $existingRecord | Remove-DnsServerResourceRecord -ZoneName $DomainName -Force
        }
        Add-DnsServerResourceRecordA -ZoneName $DomainName -Name $recordName -IPv4Address $ip | Out-Null
    }

    $spn = "HTTP/$ProxyHost"
    $account = "$DomainNetbiosName\$RunnerAccount"
    & setspn.exe -U -S $spn $account | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "setspn failed for $spn on $account"
    }

    $query = & setspn.exe -Q $spn 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $query -notmatch [regex]::Escape($spn)) {
        throw "SPN query failed for $spn. Output: $query"
    }

    Resolve-DnsName $ProxyHost -Type A -ErrorAction Stop | Out-Null
    Write-Host "DNS/SPN ready: $ProxyHost -> $ip ; $spn -> $account"
}

function Install-GitHubRunner {
    param([Parameter(Mandatory = $true)]$RunnerIdentity)

    if ($RunnerIdentity.ExistingService) {
        Write-Host "Existing GitHub Actions runner service $($RunnerIdentity.ExistingService.Name) detected; skipping reconfiguration."
        return
    }
    if ([string]::IsNullOrWhiteSpace($RunnerToken)) {
        throw 'RunnerToken is required for first-time Configure. Supply -RunnerToken or PXGO_RUNNER_TOKEN with a one-time repository registration token.'
    }
    if (-not $RunnerIdentity.Password) {
        throw 'runner account password is unavailable for first-time service configuration'
    }

    $expectedHash = $RunnerSha256.ToLowerInvariant()
    if ($expectedHash -notmatch '^[0-9a-f]{64}$') {
        throw 'RunnerSha256 must be an exact SHA-256 hex digest'
    }

    New-Item -ItemType Directory -Path $RunnerDirectory -Force | Out-Null
    $archive = Join-Path $env:TEMP "actions-runner-win-x64-$RunnerVersion.zip"
    $runnerUrl = "https://github.com/actions/runner/releases/download/v$RunnerVersion/actions-runner-win-x64-$RunnerVersion.zip"
    Invoke-WebRequest -Uri $runnerUrl -OutFile $archive

    $actualHash = (Get-FileHash -Path $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash) {
        Remove-Item $archive -Force -ErrorAction SilentlyContinue
        throw "runner archive SHA-256 mismatch: got $actualHash want $expectedHash"
    }

    if (-not (Test-Path (Join-Path $RunnerDirectory 'config.cmd'))) {
        Expand-Archive -Path $archive -DestinationPath $RunnerDirectory -Force
    }
    Remove-Item $archive -Force -ErrorAction SilentlyContinue

    $accountName = "$DomainNetbiosName\$RunnerAccount"
    $passwordPlain = ConvertTo-PlainText $RunnerIdentity.Password
    try {
        Push-Location $RunnerDirectory
        try {
            $runnerArgs = @(
                '--unattended',
                '--replace',
                '--disableupdate',
                '--url', $RepositoryUrl,
                '--token', $RunnerToken,
                '--name', $RunnerName,
                '--labels', 'pxgo-ad',
                '--work', '_work',
                '--runasservice',
                '--windowslogonaccount', $accountName,
                '--windowslogonpassword', $passwordPlain
            )
            & .\config.cmd @runnerArgs
            if ($LASTEXITCODE -ne 0) {
                throw "GitHub Actions runner configuration failed with exit code $LASTEXITCODE"
            }
        }
        finally {
            Pop-Location
        }
    }
    finally {
        $passwordPlain = $null
        [GC]::Collect()
    }

    $service = Get-CimInstance Win32_Service |
        Where-Object { $_.Name -like 'actions.runner.*' } |
        Select-Object -First 1
    if (-not $service) {
        throw 'GitHub Actions runner service was not created'
    }
    Start-Service -Name $service.Name
    Write-Host "GitHub Actions runner service ready: $($service.Name) as $($service.StartName)"
}

function Test-Lab {
    Ensure-DomainReady

    $user = Get-ADUser -Identity $RunnerAccount -Properties UserPrincipalName, ServicePrincipalName
    if (-not $user.Enabled) {
        throw "runner account $RunnerAccount is disabled"
    }

    $spn = "HTTP/$ProxyHost"
    if ($user.ServicePrincipalName -notcontains $spn) {
        throw "runner account does not own required SPN $spn"
    }

    $dns = Resolve-DnsName $ProxyHost -Type A -ErrorAction Stop
    if (-not $dns) {
        throw "DNS lookup failed for $ProxyHost"
    }

    $duplicateOutput = & setspn.exe -X 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "duplicate SPN check failed: $duplicateOutput"
    }
    if ($duplicateOutput -match '(?i)duplicate SPN' -and $duplicateOutput -notmatch '(?i)No duplicate SPN') {
        throw "duplicate SPN detected: $duplicateOutput"
    }

    $service = Get-CimInstance Win32_Service |
        Where-Object { $_.Name -like 'actions.runner.*' } |
        Select-Object -First 1
    if (-not $service) {
        throw 'GitHub Actions runner service is not installed'
    }
    if ($service.State -ne 'Running') {
        throw "GitHub Actions runner service is not running: $($service.State)"
    }

    $expectedAccount = "$DomainNetbiosName\$RunnerAccount"
    if (-not $service.StartName.Equals($expectedAccount, [StringComparison]::OrdinalIgnoreCase)) {
        throw "runner service identity $($service.StartName) does not match $expectedAccount"
    }

    Write-Host 'PxGo AD fixture validation PASS'
    Write-Host "  Domain: $DomainName"
    Write-Host "  Proxy host/SPN: $ProxyHost / $spn"
    Write-Host "  Runner: $($service.Name) ($($service.StartName))"
}

Assert-Administrator
Assert-ProxyHostInDomain

switch ($Stage) {
    'Promote' {
        Ensure-ADForest
    }
    'Configure' {
        $identity = Ensure-RunnerIdentity
        Ensure-ProxyDNSAndSPN -RunnerIdentity $identity
        Install-GitHubRunner -RunnerIdentity $identity
        Test-Lab
    }
    'Validate' {
        Test-Lab
    }
}
