[CmdletBinding()]
param(
    [string]$BuildRoot,
    [string]$OutputDirectory,
    [ValidateRange(1, 16)]
    [int]$Parallelism = 2,
    [switch]$ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$CoreBuilder = Join-Path $PSScriptRoot 'Build-ThroneCore.ps1'

if (-not (Test-Path -LiteralPath $CoreBuilder)) {
    throw "Core builder not found: $CoreBuilder"
}

if ([string]::IsNullOrWhiteSpace($BuildRoot)) {
    if (-not [string]::IsNullOrWhiteSpace($env:THRONE_CORE_BUILDER_ROOT)) {
        $BuildRoot = $env:THRONE_CORE_BUILDER_ROOT
    } else {
        $BuildRoot = Join-Path $RepoRoot '.core-builder'
    }
}
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $RepoRoot 'SGuardBuilds'
}

$BuildRoot = [IO.Path]::GetFullPath($BuildRoot)
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$WorkOutput = Join-Path $BuildRoot 'sguard-targets'
$GeneratedBuilder = Join-Path $PSScriptRoot '.Build-ThroneCore.SGuard.generated.ps1'

function Write-Step([string]$Message) {
    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function New-Directory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

Write-Host 'IRSpeedy SGuard four-file builder' -ForegroundColor Green
Write-Host "Repository: $RepoRoot"
Write-Host "Builder cache: $BuildRoot"
Write-Host "Output: $OutputDirectory"
Write-Host "Parallelism: $Parallelism"

# Normalize the source to LF so the two guarded replacements work regardless of
# the user's Git autocrlf setting.
$source = [IO.File]::ReadAllText($CoreBuilder).Replace("`r`n", "`n")

$modernOld = 'Build-CoreTarget ''windows-amd64'' $modernGo ''amd64'' "$BaseTags,with_purego,with_naive_outbound" 0x8664 $serverDirectory'
$modernNew = 'Build-CoreTarget ''windows-amd64'' $modernGo ''amd64'' "$BaseTags,with_purego" 0x8664 $serverDirectory'
if (-not $source.Contains($modernOld)) {
    throw 'Could not locate the modern Core tag line in Build-ThroneCore.ps1. Refusing to build an unknown configuration.'
}
$source = $source.Replace($modernOld, $modernNew)

$cronetOld = @"
Write-Step 'Downloading the modern Core runtime companion'
`$cronet = Join-Path `$OutputDirectory 'windows-amd64\libcronet.dll'
Download-File 'https://github.com/SagerNet/cronet-go/releases/latest/download/libcronet-windows-amd64.dll' `$cronet
"@.Replace("`r`n", "`n")
$cronetNew = @"
Write-Step 'Skipping Cronet for IRSpeedy SGuard output'
# SGuard64 is built without with_naive_outbound, so no libcronet.dll is packaged.
"@.Replace("`r`n", "`n")
if (-not $source.Contains($cronetOld)) {
    throw 'Could not locate the Cronet block in Build-ThroneCore.ps1. Refusing to build an unknown configuration.'
}
$source = $source.Replace($cronetOld, $cronetNew)

[IO.File]::WriteAllText($GeneratedBuilder, $source, (New-Object Text.UTF8Encoding($false)))

try {
    if (Test-Path -LiteralPath $WorkOutput) {
        Remove-Item -LiteralPath $WorkOutput -Recurse -Force
    }

    $argsList = @(
        '-NoLogo',
        '-NoProfile',
        '-ExecutionPolicy', 'Bypass',
        '-File', $GeneratedBuilder,
        '-BuildRoot', $BuildRoot,
        '-OutputDirectory', $WorkOutput,
        '-Parallelism', $Parallelism
    )
    if ($ValidateOnly) {
        $argsList += '-ValidateOnly'
    }

    & powershell.exe @argsList
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        throw "Underlying Core builder failed with exit code $code."
    }
} finally {
    Remove-Item -LiteralPath $GeneratedBuilder -Force -ErrorAction SilentlyContinue
}

if ($ValidateOnly) {
    Write-Host 'SGuard builder validation passed. No downloads or builds were performed.' -ForegroundColor Green
    exit 0
}

Write-Step 'Creating the four IRSpeedy SGuard executables'
New-Directory $OutputDirectory
foreach ($name in @('SGuard64.exe', 'SGuard764.exe', 'SGuard732.exe', 'SGuard32.exe')) {
    Remove-Item -LiteralPath (Join-Path $OutputDirectory $name) -Force -ErrorAction SilentlyContinue
}

$modern = Join-Path $WorkOutput 'windows-amd64\ThroneCore.exe'
$legacy64 = Join-Path $WorkOutput 'windowslegacy-amd64\ThroneCore.exe'
$legacy32 = Join-Path $WorkOutput 'windowslegacy-386\ThroneCore.exe'

foreach ($required in @($modern, $legacy64, $legacy32)) {
    if (-not (Test-Path -LiteralPath $required)) {
        throw "Expected Core output was not produced: $required"
    }
}

Copy-Item -LiteralPath $modern -Destination (Join-Path $OutputDirectory 'SGuard64.exe') -Force
Copy-Item -LiteralPath $legacy64 -Destination (Join-Path $OutputDirectory 'SGuard764.exe') -Force
Copy-Item -LiteralPath $legacy32 -Destination (Join-Path $OutputDirectory 'SGuard732.exe') -Force
Copy-Item -LiteralPath $legacy32 -Destination (Join-Path $OutputDirectory 'SGuard32.exe') -Force

Write-Step 'Validating probe-mode support'
$sguard64 = Join-Path $OutputDirectory 'SGuard64.exe'
$oldPreference = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
try {
    $helpText = (& $sguard64 -h 2>&1 | Out-String)
    $helpExit = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $oldPreference
}
if ($helpExit -ne 0) {
    throw "SGuard64.exe -h failed with exit code $helpExit."
}
if ($helpText -notmatch '(?im)probe-mode') {
    throw 'Built SGuard64.exe does not expose --probe-mode. The source branch is not ready for IRSpeedy probe validation.'
}

Write-Host '--probe-mode is present.' -ForegroundColor Green
Write-Host "`nSGuard build completed successfully." -ForegroundColor Green
Write-Host "Output directory: $OutputDirectory" -ForegroundColor Green
Get-ChildItem -LiteralPath $OutputDirectory -Filter 'SGuard*.exe' -File |
    Sort-Object Name |
    ForEach-Object {
        Write-Host ("{0,-16} {1,12} bytes  SHA256={2}" -f $_.Name, $_.Length, (Get-Sha256 $_.FullName))
    }
