# ThroneCore local Windows builder

This branch includes a local Windows builder for ThroneCore `1.3.0-beta.1` plus an IRSpeedy SGuard output mode.
It does not build the Qt application.

The Core source is based on `update/throne-1.3.0-beta.1-protorpc`. The previous custom Xray Hysteria2 implementation and the `Core: xray/sing-box` selector are not included. Hysteria2 Gecko obfs comes only from the official Throne dependencies pinned by `core/server/go.mod`.

## ThroneBuilder.exe

Run `build-thronebuilder.cmd` once to compile `ThroneBuilder.exe` with the .NET Framework C# compiler that ships with Windows/.NET Framework.

The GUI offers two choices:

1. `ThroneCore - 3 target folders`
2. `SGuard - 4 files for IRSpeedyVPN`

The same choices are also available from `build-core.cmd` if you prefer the console menu.

## SGuard output

The SGuard option writes exactly these four executables to `SGuardBuilds`:

- `SGuard64.exe` - modern Windows x64
- `SGuard764.exe` - Windows 7 x64
- `SGuard732.exe` - Windows 7 x86
- `SGuard32.exe` - copy of the Windows 7 x86 Core for the existing IRSpeedy naming scheme

The SGuard modern x64 build intentionally omits `with_naive_outbound`, so `libcronet.dll` is not part of the IRSpeedy output.
The builder also verifies that `SGuard64.exe -h` exposes `--probe-mode` before reporting success.

## ThroneCore output

The original three-target mode is unchanged and writes below `CoreBuilds`:

| Directory | Target |
| --- | --- |
| `windows-amd64` | Modern Windows x64 |
| `windowslegacy-amd64` | Windows 7 x64 |
| `windowslegacy-386` | Windows 7 x86 |

The original modern ThroneCore package includes `libcronet.dll`. `SHA256SUMS.txt` and `BUILD-INFO.txt` record the generated file hashes and pinned source/toolchain versions.

## Requirements

1. Windows.
2. Git for Windows available in `PATH`.
3. At least 5 GB free disk space.
4. .NET Framework 4.x only if you want to compile/use `ThroneBuilder.exe`; the console builders do not require the GUI executable.

The first run downloads and caches the pinned toolchains and dependencies. Later builds reuse that cache.

## Put the cache on another drive

The cache defaults to `.core-builder` in the repository. To use another drive, set this once in Command Prompt:

```bat
setx THRONE_CORE_BUILDER_ROOT D:\ThroneCoreBuilder
```

Open a new Command Prompt or Explorer window after running `setx`, then launch the builder again.

If the official Go module proxy fails, the builder retries once with `https://goproxy.cn,direct` while retaining normal `go.sum` verification. You can instead set a trusted proxy explicitly:

```bat
setx THRONE_GO_PROXY https://your-trusted-go-proxy.example,direct
```

## Reproducibility and source safety

- Official Go `1.26.7` builds the modern target.
- The checksum-pinned `go-legacy-win7 1.26.6-1` toolchain builds both Windows 7 targets.
- Protobuf and ProtoRPC sources are regenerated with pinned generator versions.
- The official Throne `go.mod` and `go.sum` select sing-box, Xray and Gecko support; the builder adds no dependency replacements.
- The builder copies Core sources into an isolated staging directory. It performs generation and compilation only inside that staging copy.
- No AppVeyor service, API, or agent is used.

For script validation without downloads or compilation:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\tools\Build-ThroneCore.ps1 -ValidateOnly
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\tools\Build-SGuard.ps1 -ValidateOnly
```
