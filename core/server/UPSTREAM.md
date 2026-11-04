# Custom desktop Core upstream baseline

- Throne release: 1.4.0-beta.6 (pre-release)
- Upstream commit: 4b1904e3252e4a99c25a91dbfc15b9c10fe77f1f
- Previous upstream baseline: 1.3.1, 825b9809eb29b2a31fa29887abb7354cde54e9b7
- Previous custom commit: 4219783d7bbdba28f334340c39288a6f9a02dac3

This is a core-only port onto the existing desktop layout, not a replacement
with the upstream application's tree.

## Ported from 1.3.1 to 1.4.0-beta.6

- Updated upstream dependency pins: sing-box f25a530dc468 (required version
  v1.15.0-alpha.13), Xray 1c6eb7771c4d, sing e3e392877db8, sing-tun and
  wireguard-go e11787583013. Desktop-only Hysteria core/extras v2.12.3,
  apernet/quic-go 73339f7edbb9, Cobra and the local ProtoRPC replacement remain.
- Ported the scoped Box lifecycle/reference manager and refreshed the copied
  native DNS client/router while retaining provider-owned race caches and diagnostics.
- Adapted composite DNS transports and the custom round-robin selector to the
  new lifecycle/group interfaces. Routed connections still dial through the
  custom pool; ordinary rotation retains live connections and offline guards.
- Ported Xray gate port reservation, fresh-port recycling, startup cooldown and
  instance restart fixes, retaining custom Hysteria constructors and DNS setup.
- Mapped upstream's multi-path process owner to the existing single-path RPC.
- Native DNSTT/VayDNS and MasterDNS constructors are registered by the updated
  sing-box dependency; configuration and invalid-key checks cover their registration.
  This does not add link import, UI, XDNS sidecars or new RPCs to IRSPEEDY-WIN.

## Preserved

- ProtoRPC schemas, transport, probe mode and parent-process lifecycle.
- Custom Hysteria core/extras v2.12.3, Gecko packet adapter fix, Xray integration,
  diagnostics, custom DNS, and existing GUI/configuration behavior.
- Existing desktop directory layout consumed by the automatic Windows builder.

The upstream app's IPC/framing, privilege-response fields, new guard process,
scanner/headless RPCs, mobile bindings and GUI/config-generation changes are not
part of this desktop core-only port. The existing RPC schemas, build identity,
parent checks and Windows DNS monitor remain intact. No workflow or builder
configuration is changed. The custom pool's health/ranking/recovery algorithm
is retained, rather than replaced by the upstream selector's newer policy.

Modern Core/GUI packaging must copy libcronet.dll from the selected
github.com/sagernet/cronet-go/lib/windows_amd64 module, matching the new binding,
instead of downloading an unrelated latest DLL. SGuard excludes Cronet.

Source changes are in Throne-G on agent/core-startup-diagnostics-fix. Rebuild
the native Core payloads with the existing builder, then package SGuard32/64 and
SGuard7 variants using their existing feature sets. IRSPEEDY-WIN's runtime source
and version are unchanged; its native-test overlay is adapted to the new lifecycle.
Its Codegraph does not map this repository's Go internals.

## Validation of this port

- Passed: focused Core tests with `-race -tags with_quic`, including the root
  ProtoRPC/metadata tests, DNS client/race/probe, Box, pool/recovery, diagnostics
  and custom Xray/Hysteria tests. The existing DNS Regression Guard test and vet
  commands also pass without QUIC tags.
- Passed: the current Windows app's 111 DNS configuration checks, then their
  generated fixtures through the actual Core tests and HTTP/SOCKS IPv4 policy overlay.
- Passed: Windows amd64 modern (including purego/Naive, OpenVPN/OpenConnect)
  and 386 Core cross-builds with Go 1.27.2; Windows DNS and Xray resolver test
  executables cross-compile. These are compile checks, not Windows execution.
- Blocked locally: two xraydns integration fixtures need a netlink socket;
  this host denies AF_NETLINK/NETLINK_ROUTE with EPERM before Box startup.
  Assertions were not removed or skipped. Windows runtime, patched Windows 7
  toolchain builds, packaged payload/Single-file app and live VPN are Not Run.
