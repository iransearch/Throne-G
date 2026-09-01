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

$ModernGoVersion = '1.26.7'
$LegacyGoVersion = '1.26.6-1'
$LegacyGoSha256 = 'a0fb26ae90b33dd223da09f5f4476237d65bb185acbf12e57cdaba90d32257f3'
$ProtocVersion = '31.1'
$ProtocGenGoVersion = 'v1.36.11'
$ProtocGenProtoRpcVersion = 'v1.1.4'
$BaseTags = 'with_clash_api,with_gvisor,with_quic,with_wireguard,with_utls,with_dhcp,with_tailscale,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0'
$script:AllowGoProxyFallback = $false
$script:GoProxyFallbackUsed = $false

$RepoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($BuildRoot)) {
    if (-not [string]::IsNullOrWhiteSpace($env:THRONE_CORE_BUILDER_ROOT)) {
        $BuildRoot = $env:THRONE_CORE_BUILDER_ROOT
    } else {
        $BuildRoot = Join-Path $RepoRoot '.core-builder'
    }
}
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $RepoRoot 'CoreBuilds'
}

$BuildRoot = [IO.Path]::GetFullPath($BuildRoot)
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$DownloadsDirectory = Join-Path $BuildRoot 'downloads'
$ToolchainsDirectory = Join-Path $BuildRoot 'toolchains'
$ToolsDirectory = Join-Path $BuildRoot 'tools'
$ModuleCache = Join-Path $BuildRoot 'go-mod-cache'
$BuildCache = Join-Path $BuildRoot 'go-build-cache'
$TemporaryDirectory = Join-Path $BuildRoot 'tmp'
$StagingDirectory = Join-Path $BuildRoot 'staging'

function Write-Step([string]$Message) {
    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

function Invoke-Native {
    param([string]$FilePath)
    $nativeArguments = @($args)
    & $FilePath @nativeArguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code $LASTEXITCODE`: $FilePath $($nativeArguments -join ' ')"
    }
}

function Invoke-Go {
    param([string]$GoExecutable)
    $goArguments = @($args)
    try {
        Invoke-Native $GoExecutable @goArguments
    } catch {
        if (-not $script:AllowGoProxyFallback -or $script:GoProxyFallbackUsed) {
            throw
        }
        $script:GoProxyFallbackUsed = $true
        $env:GOPROXY = 'https://goproxy.cn,direct'
        Write-Warning 'The default Go module proxy failed. Retrying with https://goproxy.cn and normal go.sum verification.'
        Invoke-Native $GoExecutable @goArguments
    }
}

function New-Directory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Test-IsChildPath([string]$Path, [string]$Parent) {
    $fullPath = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $fullParent = [IO.Path]::GetFullPath($Parent).TrimEnd('\')
    return $fullPath.StartsWith($fullParent + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Remove-BuilderDirectory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    if (-not (Test-IsChildPath $Path $BuildRoot)) {
        throw "Refusing to remove a directory outside the builder root: $Path"
    }
    Remove-Item -LiteralPath $Path -Recurse -Force
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Download-File {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$Destination,
        [string]$Sha256
    )

    if (Test-Path -LiteralPath $Destination) {
        if ([string]::IsNullOrWhiteSpace($Sha256) -or (Get-Sha256 $Destination) -eq $Sha256.ToLowerInvariant()) {
            Write-Host "Using cached download: $Destination"
            return
        }
        Remove-Item -LiteralPath $Destination -Force
    }

    New-Directory (Split-Path -Parent $Destination)
    $partial = "$Destination.download"
    if (Test-Path -LiteralPath $partial) {
        Remove-Item -LiteralPath $partial -Force
    }

    Write-Host "Downloading $Uri"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        $client = New-Object Net.WebClient
        try {
            if ($null -ne $client.Proxy) {
                $client.Proxy.Credentials = [Net.CredentialCache]::DefaultNetworkCredentials
            }
            $client.DownloadFile($Uri, $partial)
            break
        } catch {
            if (Test-Path -LiteralPath $partial) {
                Remove-Item -LiteralPath $partial -Force
            }
            if ($attempt -eq 3) {
                throw
            }
            Write-Warning "Download attempt $attempt failed. Retrying..."
            Start-Sleep -Seconds (2 * $attempt)
        } finally {
            $client.Dispose()
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($Sha256)) {
        $actual = Get-Sha256 $partial
        if ($actual -ne $Sha256.ToLowerInvariant()) {
            Remove-Item -LiteralPath $partial -Force
            throw "Checksum mismatch for $Uri. Expected $Sha256, received $actual."
        }
    }
    Move-Item -LiteralPath $partial -Destination $Destination
}

function Expand-ZipFresh([string]$Archive, [string]$Destination) {
    Remove-BuilderDirectory $Destination
    New-Directory $Destination
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [IO.Compression.ZipFile]::ExtractToDirectory($Archive, $Destination)
}

function Find-GoExecutable([string]$Root) {
    $go = Get-ChildItem -LiteralPath $Root -Recurse -Filter go.exe -File |
        Where-Object { $_.FullName -match '\\bin\\go\.exe$' } |
        Select-Object -First 1
    if ($null -eq $go) {
        throw "go.exe was not found below $Root"
    }
    return $go.FullName
}

function Set-GoEnvironment {
    param(
        [Parameter(Mandatory = $true)][string]$GoExecutable,
        [Parameter(Mandatory = $true)][string]$CacheSuffix
    )
    $goRoot = Split-Path -Parent (Split-Path -Parent $GoExecutable)
    $env:GOROOT = $goRoot
    $env:GOTOOLCHAIN = 'local'
    $env:CGO_ENABLED = '0'
    $env:GOMODCACHE = $ModuleCache
    $env:GOCACHE = Join-Path $BuildCache $CacheSuffix
    $env:GOTMPDIR = $TemporaryDirectory
    $env:GOBIN = Join-Path $ToolsDirectory 'bin'
    $env:PATH = "$(Split-Path -Parent $GoExecutable);$env:GOBIN;$env:PATH"
    New-Directory $env:GOCACHE
    New-Directory $env:GOMODCACHE
    New-Directory $env:GOTMPDIR
    New-Directory $env:GOBIN
}

function Copy-SourceTree([string]$DestinationRoot) {
    Remove-BuilderDirectory $DestinationRoot
    New-Directory (Join-Path $DestinationRoot 'core')
    Copy-Item -LiteralPath (Join-Path $RepoRoot 'core\server') -Destination (Join-Path $DestinationRoot 'core\server') -Recurse
    Copy-Item -LiteralPath (Join-Path $RepoRoot 'core\protorpc') -Destination (Join-Path $DestinationRoot 'core\protorpc') -Recurse
}

function Get-PeMachine([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    try {
        $reader = New-Object IO.BinaryReader($stream)
        $stream.Position = 0x3c
        $peOffset = $reader.ReadInt32()
        $stream.Position = $peOffset + 4
        return $reader.ReadUInt16()
    } finally {
        $stream.Dispose()
    }
}

function Build-CoreTarget {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$GoExecutable,
        [Parameter(Mandatory = $true)][string]$Architecture,
        [Parameter(Mandatory = $true)][string]$Tags,
        [Parameter(Mandatory = $true)][UInt16]$ExpectedMachine,
        [Parameter(Mandatory = $true)][string]$ServerDirectory
    )

    Write-Step "Building $Name"
    Set-GoEnvironment -GoExecutable $GoExecutable -CacheSuffix $Name
    $env:GOOS = 'windows'
    $env:GOARCH = $Architecture

    Push-Location $ServerDirectory
    try {
        $singVersion = (Invoke-Go $GoExecutable 'list' '-mod=mod' '-m' '-f' '{{.Version}}' 'github.com/sagernet/sing-box' | Out-String).Trim()
        if ([string]::IsNullOrWhiteSpace($singVersion)) {
            throw "Could not determine the sing-box version for $Name."
        }
        $destination = Join-Path $OutputDirectory $Name
        New-Directory $destination
        $output = Join-Path $destination 'ThroneCore.exe'
        $ldflags = "-w -s -X github.com/sagernet/sing-box/constant.Version=$singVersion -X internal/godebug.defaultGODEBUG=multipathtcp=0 -checklinkname=0"
        Invoke-Go $GoExecutable 'build' "-p=$Parallelism" '-mod=mod' '-o' $output '-trimpath' '-ldflags' $ldflags '-tags' $Tags '.'
    } finally {
        Pop-Location
    }

    $machine = Get-PeMachine $output
    if ($machine -ne $ExpectedMachine) {
        throw ('Unexpected PE machine for {0}: expected 0x{1:X4}, received 0x{2:X4}' -f $Name, $ExpectedMachine, $machine)
    }
    Write-Host "Created $output"
}

if ($env:OS -ne 'Windows_NT') {
    throw 'This one-click builder must run on Windows.'
}
if ($PSVersionTable.PSVersion.Major -lt 5) {
    throw 'PowerShell 5.1 or newer is required.'
}
if ($null -eq (Get-Command git.exe -ErrorAction SilentlyContinue)) {
    throw 'Git for Windows is required and must be available in PATH.'
}
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot 'core\server\go.mod'))) {
    throw "Run this builder from a complete Throne-G source checkout: $RepoRoot"
}
if (-not [string]::IsNullOrWhiteSpace($env:THRONE_GO_PROXY)) {
    $env:GOPROXY = $env:THRONE_GO_PROXY
} elseif ([string]::IsNullOrWhiteSpace($env:GOPROXY)) {
    $env:GOPROXY = 'https://proxy.golang.org,direct'
    $script:AllowGoProxyFallback = $true
}

Write-Host 'ThroneCore one-click three-target builder' -ForegroundColor Green
Write-Host "Repository: $RepoRoot"
Write-Host "Builder cache: $BuildRoot"
Write-Host "Output: $OutputDirectory"
Write-Host "Parallelism: $Parallelism"

if ($ValidateOnly) {
    Write-Host 'Builder validation passed. No downloads or builds were performed.' -ForegroundColor Green
    exit 0
}

New-Directory $BuildRoot
$driveRoot = [IO.Path]::GetPathRoot($BuildRoot)
$drive = New-Object IO.DriveInfo($driveRoot)
$minimumFree = 5GB
if ($drive.AvailableFreeSpace -lt $minimumFree) {
    $freeGb = [Math]::Round($drive.AvailableFreeSpace / 1GB, 2)
    throw "At least 5 GB free space is required on $driveRoot (available: $freeGb GB). Set THRONE_CORE_BUILDER_ROOT to a drive with more space and run build-core.cmd again."
}

New-Directory $DownloadsDirectory
New-Directory $ToolchainsDirectory
New-Directory $ToolsDirectory
New-Directory $OutputDirectory

$modernArchive = Join-Path $DownloadsDirectory "go$ModernGoVersion.windows-amd64.zip"
$legacyArchive = Join-Path $DownloadsDirectory "go-legacy-win7-$LegacyGoVersion.windows-amd64.zip"
$protocArchive = Join-Path $DownloadsDirectory "protoc-$ProtocVersion-win64.zip"
$modernDirectory = Join-Path $ToolchainsDirectory "go-modern-$ModernGoVersion"
$legacyDirectory = Join-Path $ToolchainsDirectory "go-legacy-$LegacyGoVersion"
$protocDirectory = Join-Path $ToolsDirectory "protoc-$ProtocVersion"

Write-Step 'Preparing pinned build toolchains'
Download-File "https://go.dev/dl/go$ModernGoVersion.windows-amd64.zip" $modernArchive
Download-File "https://github.com/thongtech/go-legacy-win7/releases/download/v$LegacyGoVersion/go-legacy-win7-$LegacyGoVersion.windows_amd64.zip" $legacyArchive $LegacyGoSha256
Download-File "https://github.com/protocolbuffers/protobuf/releases/download/v$ProtocVersion/protoc-$ProtocVersion-win64.zip" $protocArchive

if (-not (Test-Path -LiteralPath (Join-Path $modernDirectory 'go\bin\go.exe'))) {
    Expand-ZipFresh $modernArchive $modernDirectory
}
$legacyReady = $null
if (Test-Path -LiteralPath $legacyDirectory) {
    $legacyReady = Get-ChildItem -LiteralPath $legacyDirectory -Recurse -Filter go.exe -File -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match '\\bin\\go\.exe$' } |
        Select-Object -First 1
}
if ($null -eq $legacyReady) {
    Expand-ZipFresh $legacyArchive $legacyDirectory
}
if (-not (Test-Path -LiteralPath (Join-Path $protocDirectory 'bin\protoc.exe'))) {
    Expand-ZipFresh $protocArchive $protocDirectory
}

$modernGo = Find-GoExecutable $modernDirectory
$legacyGo = Find-GoExecutable $legacyDirectory
$protoc = Join-Path $protocDirectory 'bin\protoc.exe'
Invoke-Native $modernGo version
Invoke-Native $legacyGo version
Invoke-Native $protoc --version

Write-Step 'Installing pinned protobuf generators'
Set-GoEnvironment -GoExecutable $modernGo -CacheSuffix 'host-tools'
Invoke-Go $modernGo install "google.golang.org/protobuf/cmd/protoc-gen-go@$ProtocGenGoVersion"
Invoke-Go $modernGo install "github.com/chai2010/protorpc/protoc-gen-protorpc@$ProtocGenProtoRpcVersion"

Write-Step 'Preparing an isolated source copy'
Copy-SourceTree $StagingDirectory
$serverDirectory = Join-Path $StagingDirectory 'core\server'
$genDirectory = Join-Path $serverDirectory 'gen'
Set-GoEnvironment -GoExecutable $modernGo -CacheSuffix 'host-tools'
$env:PATH = "$(Split-Path -Parent $protoc);$env:PATH"
Push-Location $genDirectory
try {
    Invoke-Native $protoc -I . --go_out=. --protorpc_out=. libcore.proto
} finally {
    Pop-Location
}

Build-CoreTarget 'windows-amd64' $modernGo 'amd64' "$BaseTags,with_purego,with_naive_outbound" 0x8664 $serverDirectory
Build-CoreTarget 'windowslegacy-amd64' $legacyGo 'amd64' $BaseTags 0x8664 $serverDirectory
Build-CoreTarget 'windowslegacy-386' $legacyGo '386' $BaseTags 0x014c $serverDirectory

Write-Step 'Downloading the modern Core runtime companion'
$cronet = Join-Path $OutputDirectory 'windows-amd64\libcronet.dll'
Download-File 'https://github.com/SagerNet/cronet-go/releases/latest/download/libcronet-windows-amd64.dll' $cronet

Write-Step 'Writing checksums and build information'
$sourceCommit = (& git.exe -C $RepoRoot rev-parse HEAD | Out-String).Trim()
$dirty = -not [string]::IsNullOrWhiteSpace((& git.exe -C $RepoRoot status --porcelain | Out-String).Trim())
$checksumLines = New-Object Collections.Generic.List[string]
$infoLines = New-Object Collections.Generic.List[string]
$infoLines.Add("Source commit: $sourceCommit")
$infoLines.Add("Source dirty: $dirty")
$infoLines.Add("Built at UTC: $([DateTime]::UtcNow.ToString('o'))")
$infoLines.Add("Modern Go: $ModernGoVersion")
$infoLines.Add("Windows 7 Go: $LegacyGoVersion")
$infoLines.Add('Dependencies: official source go.mod/go.sum (no builder overrides)')

foreach ($file in Get-ChildItem -LiteralPath $OutputDirectory -Recurse -File | Sort-Object FullName) {
    if ($file.Name -in @('SHA256SUMS.txt', 'BUILD-INFO.txt')) {
        continue
    }
    $relative = $file.FullName.Substring($OutputDirectory.Length).TrimStart('\').Replace('\', '/')
    $checksumLines.Add("$(Get-Sha256 $file.FullName)  $relative")
}
[IO.File]::WriteAllLines((Join-Path $OutputDirectory 'SHA256SUMS.txt'), $checksumLines, [Text.Encoding]::ASCII)
[IO.File]::WriteAllLines((Join-Path $OutputDirectory 'BUILD-INFO.txt'), $infoLines, [Text.Encoding]::UTF8)

Write-Host "`nAll three Core builds completed successfully." -ForegroundColor Green
Write-Host "Output directory: $OutputDirectory" -ForegroundColor Green
Get-ChildItem -LiteralPath $OutputDirectory -Recurse -File |
    Select-Object FullName, Length |
    Format-Table -AutoSize
