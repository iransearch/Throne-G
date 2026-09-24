# Custom desktop Core upstream baseline

- Throne release: 1.3.1
- Upstream commit: 825b9809eb29b2a31fa29887abb7354cde54e9b7
- Previous custom commit: f8cef6a6f81c74e1734b1078d9b35d0445bcb528

This is a core-only port onto the existing desktop layout, not a replacement
with the upstream application's tree.

## Ported from 1.3.0 to 1.3.1

- Updated existing dependency requirements and upstream replacements, including
  sing-box, sing, sing-tun, wireguard-go, bbolt and Cronet.
- Applied the active Linux TUN auto-redirect exemption mark to temporary probe
  boxes, without overriding an explicit mark or a probe's own TUN.
- Added the Windows TUN egress forwarding watcher and its Start/Stop cleanup.
- Recognized the complete Linux capability set as sufficient for TUN operation.

## Preserved

- ProtoRPC schemas, transport, probe mode and parent-process lifecycle.
- Custom Hysteria core/extras v2.12.3, Gecko packet adapter fix, Xray integration,
  diagnostics, custom DNS, and existing GUI/configuration behavior.
- Existing desktop directory layout consumed by the automatic Windows builder.

Upstream mobile bindings, the mobile-only probe interface abstraction and
parent-check bypass, GUI subscription/config-generation changes, and the
ruleset helper extraction are not part of this desktop core-only port.
The existing RPC schema is unchanged.

Modern Core/GUI packaging must copy libcronet.dll from the selected
github.com/sagernet/cronet-go/lib/windows_amd64 module, matching upstream 1.3.1,
instead of downloading an unrelated latest DLL. SGuard excludes Cronet.

Validation is source/dependency review; Windows builds and runtime validation
are performed by the user with the builder.
