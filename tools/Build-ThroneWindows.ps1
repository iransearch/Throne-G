[CmdletBinding()]
param(
    [string]$BuildRoot,
    [string]$OutputDirectory,
    [ValidateRange(1, 16)][int]$Parallelism = 2,
    [switch]$ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$BuilderRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($BuildRoot)) {
    $BuildRoot = $env:THRONE_CORE_BUILDER_ROOT
    if ([string]::IsNullOrWhiteSpace($BuildRoot)) { $BuildRoot = Join-Path $BuilderRoot '.core-builder' }
}
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) { $OutputDirectory = Join-Path $BuilderRoot 'ThroneBuilds' }
$BuildRoot = [IO.Path]::GetFullPath($BuildRoot)
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)

function Invoke-Checked {
    param([string]$Executable)
    & $Executable @args
    if ($LASTEXITCODE -ne 0) { throw "Command failed with exit code ${LASTEXITCODE}: $Executable" }
}

function New-Folder([string]$Path) {
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}

function Find-Tool([string]$Name, [string[]]$Candidates) {
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    foreach ($candidate in $Candidates) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    throw "$Name was not found. Install it and run the builder again."
}

function Download([string]$Url, [string]$Destination) {
    if (Test-Path -LiteralPath $Destination) {
        Write-Host "Using cached download: $Destination"
        return
    }
    New-Folder (Split-Path -Parent $Destination)
    $partial = $Destination + '.partial'
    Invoke-Checked $script:Curl '--fail' '--location' '--retry' '3' '--output' $partial $Url
    Move-Item -LiteralPath $partial -Destination $Destination -Force
}

function Import-MsvcEnvironment([string]$VsDevCmd) {
    # Only the VS environment setup uses cmd. CMake/Ninja arguments are passed
    # directly to PowerShell native invocation, including --parallel's value.
    $commandLine = 'call "' + $VsDevCmd + '" -no_logo -arch=amd64 -host_arch=amd64 >nul && set'
    $lines = & $env:ComSpec /d /s /c $commandLine
    if ($LASTEXITCODE -ne 0) { throw 'Visual Studio x64 environment initialization failed.' }
    foreach ($line in $lines) {
        $separator = $line.IndexOf('=')
        if ($separator -gt 0) {
            [Environment]::SetEnvironmentVariable($line.Substring(0, $separator), $line.Substring($separator + 1), 'Process')
        }
    }
}

if ($env:OS -ne 'Windows_NT') { throw 'This builder must run on Windows.' }
if ($PSVersionTable.PSVersion.Major -lt 5) { throw 'PowerShell 5.1 or newer is required.' }
$null = Find-Tool 'git.exe' @()
$script:Curl = Find-Tool 'curl.exe' @()
$vswhere = Find-Tool 'vswhere.exe' @("${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe")
$vs = (& $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($vs)) {
    throw 'Install Visual Studio 2022 Build Tools with Desktop development with C++ and a Windows SDK.'
}
$vsDevCmd = Join-Path $vs 'Common7\Tools\VsDevCmd.bat'
if (-not (Test-Path -LiteralPath $vsDevCmd)) { throw "Missing Visual Studio environment script: $vsDevCmd" }
$cmake = Find-Tool 'cmake.exe' @("$env:ProgramFiles\CMake\bin\cmake.exe", (Join-Path $vs 'Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe'))
$sevenZip = Find-Tool '7z.exe' @("$env:ProgramFiles\7-Zip\7z.exe", "${env:ProgramFiles(x86)}\7-Zip\7z.exe")
$coreBuilder = Join-Path $PSScriptRoot 'Build-ThroneCore.ps1'
if (-not (Test-Path -LiteralPath $coreBuilder)) { throw "Missing Core builder: $coreBuilder" }
Write-Host "Visual Studio: $vs"
Write-Host "CMake: $cmake"
Write-Host "Builder cache: $BuildRoot"
Write-Host "Output: $OutputDirectory"
if ($ValidateOnly) {
    Write-Host 'GUI builder prerequisites found. No downloads or builds were performed.' -ForegroundColor Green
    exit 0
}

New-Folder $BuildRoot
$drive = New-Object IO.DriveInfo([IO.Path]::GetPathRoot($BuildRoot))
if ($drive.AvailableFreeSpace -lt 5GB) { throw 'At least 5 GB free space is required on the builder cache drive.' }
if ($drive.AvailableFreeSpace -lt 10GB) { Write-Warning '10 GB free space is recommended for the full GUI build.' }

# A fresh per-run directory prevents packaging stale binaries after a failure.
$runRoot = Join-Path (Join-Path $BuildRoot 'full-throne') ([Guid]::NewGuid().ToString('N'))
New-Folder $runRoot
$coreOutput = Join-Path $runRoot 'core-output'
Write-Host "`n==> Building modern x64 Core from the latest custom branch"
Invoke-Checked 'powershell.exe' -NoLogo -NoProfile -ExecutionPolicy Bypass -File $coreBuilder -BuildRoot $BuildRoot -OutputDirectory $coreOutput -Parallelism $Parallelism -ModernOnly
$buildInfo = Get-Content -LiteralPath (Join-Path $coreOutput 'BUILD-INFO.txt')
$commitLine = @($buildInfo | Where-Object { $_.StartsWith('Source commit: ') })
$checkoutLine = @($buildInfo | Where-Object { $_.StartsWith('Source checkout: ') })
if ($commitLine.Count -ne 1 -or $checkoutLine.Count -ne 1) { throw 'Core build metadata is missing. Update all builder files together.' }
$commit = $commitLine[0].Substring('Source commit: '.Length)
$sourceRoot = $checkoutLine[0].Substring('Source checkout: '.Length)
if ($commit -notmatch '^[0-9a-f]{40}$') { throw 'Invalid Core commit in build metadata.' }
$guiCommit = (& git.exe -C $sourceRoot rev-parse HEAD | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $guiCommit -ne $commit) { throw 'GUI and Core source commits differ. Build stopped.' }

Write-Host "`n==> Preparing Qt and GUI tools"
$downloads = Join-Path $BuildRoot 'downloads'
$qtVersion = '6.11.1'
$qtRoot = Join-Path $BuildRoot "gui-tools\qt-$qtVersion-x64"
$qtArchive = Join-Path $downloads "Qt_${qtVersion}_x64.7z"
Download "https://github.com/throneproj/buildqt/releases/download/Qt_$qtVersion/Qt_${qtVersion}_x64.7z" $qtArchive
$qtPrefix = Join-Path $qtRoot 'Qt'
if (-not (Test-Path -LiteralPath (Join-Path $qtRoot 'READY'))) {
    New-Folder $qtRoot
    Invoke-Checked $sevenZip x $qtArchive "-o$qtRoot" -y
    if (-not (Test-Path -LiteralPath (Join-Path $qtPrefix 'lib\cmake\Qt6\Qt6Config.cmake'))) { throw 'Qt package does not contain Qt6Config.cmake at the expected path.' }
    New-Item -ItemType File -Path (Join-Path $qtRoot 'READY') -Force | Out-Null
}
$opensslRoot = Join-Path $BuildRoot 'gui-tools\openssl-x64'
$opensslArchive = Join-Path $downloads 'openssl_x64.7z'
Download 'https://github.com/throneproj/env_windows_legacy/releases/download/latest/openssl_x64.7z' $opensslArchive
if (-not (Test-Path -LiteralPath (Join-Path $opensslRoot 'READY'))) {
    New-Folder $opensslRoot
    Invoke-Checked $sevenZip x $opensslArchive "-o$opensslRoot" -y
    if (-not (Test-Path -LiteralPath (Join-Path $opensslRoot 'openssl'))) { throw 'OpenSSL package does not contain the expected openssl folder.' }
    New-Item -ItemType File -Path (Join-Path $opensslRoot 'READY') -Force | Out-Null
}
$ninjaRoot = Join-Path $BuildRoot 'gui-tools\ninja-1.13.2'
$ninjaArchive = Join-Path $downloads 'ninja-win-1.13.2.zip'
Download 'https://github.com/ninja-build/ninja/releases/download/v1.13.2/ninja-win.zip' $ninjaArchive
if (-not (Test-Path -LiteralPath (Join-Path $ninjaRoot 'ninja.exe'))) {
    Expand-Archive -LiteralPath $ninjaArchive -DestinationPath $ninjaRoot -Force
}
$ninja = Join-Path $ninjaRoot 'ninja.exe'
$guiBuild = Join-Path $runRoot 'gui-build'
New-Folder $guiBuild
Download 'https://raw.githubusercontent.com/throneproj/routeprofiles/rule-set/srslist.h' (Join-Path $guiBuild 'srslist.h')

Write-Host "`n==> Building Throne GUI from commit $commit"
Import-MsvcEnvironment $vsDevCmd
$env:PATH = "$qtPrefix\bin;$ninjaRoot;$env:PATH"
$env:OPENSSL_ROOT_DIR = Join-Path $opensslRoot 'openssl'
$env:INPUT_VERSION = 'custom-' + $commit.Substring(0, 8)
Invoke-Checked $cmake -S $sourceRoot -B $guiBuild -G Ninja '-DCMAKE_BUILD_TYPE=Release' "-DCMAKE_MAKE_PROGRAM=$ninja" "-DCMAKE_PREFIX_PATH=$qtPrefix" "-DOPENSSL_ROOT_DIR=$env:OPENSSL_ROOT_DIR" '-DSPB_PROTO_USE_CLANG_FORMAT=OFF'
Invoke-Checked $cmake --build $guiBuild --config Release --parallel $Parallelism

Write-Host "`n==> Packaging portable Throne"
New-Folder $OutputDirectory
$packageName = 'Throne-windows-x64-' + $commit.Substring(0, 8) + '-' + (Split-Path -Leaf $runRoot).Substring(0, 8)
$portable = Join-Path $OutputDirectory $packageName
New-Folder $portable
foreach ($entry in @(
    @{ Source = (Join-Path $guiBuild 'Throne.exe'); Name = 'Throne.exe' },
    @{ Source = (Join-Path $coreOutput 'windows-amd64\ThroneCore.exe'); Name = 'ThroneCore.exe' },
    @{ Source = (Join-Path $coreOutput 'windows-amd64\libcronet.dll'); Name = 'libcronet.dll' }
)) {
    if (-not (Test-Path -LiteralPath $entry.Source)) { throw "Missing package file: $($entry.Source)" }
    Copy-Item -LiteralPath $entry.Source -Destination (Join-Path $portable $entry.Name)
}
Download 'https://github.com/throneproj/updater/releases/latest/download/updater-windows-x64.exe' (Join-Path $portable 'updater.exe')
# The official buildqt archive is normally static. Deploy Qt plugins and DLLs
# when a shared Qt archive is supplied instead.
if (Test-Path -LiteralPath (Join-Path $qtPrefix 'bin\Qt6Core.dll')) {
    $deploy = Join-Path $qtPrefix 'bin\windeployqt.exe'
    if (-not (Test-Path -LiteralPath $deploy)) { throw 'Shared Qt requires windeployqt.exe.' }
    Invoke-Checked $deploy --release --compiler-runtime (Join-Path $portable 'Throne.exe')
}
Get-ChildItem -LiteralPath (Join-Path $opensslRoot 'openssl') -Recurse -Filter '*.dll' -File |
    ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $portable -Force }
$metadata = @($buildInfo) + @("GUI source commit: $commit", "Qt: $qtVersion", 'Target: Windows x64 portable')
[IO.File]::WriteAllLines((Join-Path $portable 'BUILD-INFO.txt'), [string[]]$metadata, [Text.Encoding]::UTF8)
$hashes = @(Get-ChildItem -LiteralPath $portable -Recurse -File | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($portable.Length).TrimStart('\').Replace('\', '/')
    '{0}  {1}' -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $relative
})
[IO.File]::WriteAllLines((Join-Path $portable 'SHA256SUMS.txt'), [string[]]$hashes, [Text.Encoding]::ASCII)
$zipPath = Join-Path $OutputDirectory ($packageName + '.zip')
Invoke-Checked $sevenZip a -tzip $zipPath (Join-Path $portable '*')
Write-Host "`nFull Throne GUI + Core build completed successfully." -ForegroundColor Green
Write-Host "Portable folder: $portable" -ForegroundColor Green
Write-Host "ZIP: $zipPath" -ForegroundColor Green
Write-Host "GUI and Core built from commit: $commit" -ForegroundColor Cyan
