# Auto Selector round-robin extension

Adapted from throneproj/sing-box f154bec036c9, protocol/group/autoselector.go
and autoselector_health.go. Distributed under the upstream GPL-3.0 license.
The health implementation is retained unchanged. Successful round-robin dials
update the selected marker without interrupting established connections.
Status types alias upstream so existing QueryAutoSelectors and AutoSelectorAction RPCs keep working.

The registry adds the explicit auto-selector-round-robin outbound type, so old
Cores reject this configuration instead of silently falling back to rotate.
The original auto-selector type and its existing modes remain upstream.
The extension picks the next usable qualified member for each new dial, retaining independent TCP/UDP cursors under
the existing selector mutex. Cooldown, network support, retries and pinning retain
the original policy. Existing connections are not closed by the rotation.

Keep these source copies aligned when changing the pinned sing-box dependency.


Validation:
- `go test -race ./internal/autoselector ./internal/boxmain ./internal/boxbox`
  covers deterministic order, network-independent cursors, concurrent fairness,
  member removal/recovery, cooldown/retries/pinning, retained flows, JSON parsing
  and registration on the RPC box while AI uses the upstream concrete type.
- The Windows amd64 Core builds with the production modern feature tags.

Windows CI uses the replacements already pinned in core/server/go.mod. The
previous Gecko CI overrides lack APIs imported by the current Core, and must not
replace these dependencies. The Core branch is included in the CI branch filter.
When packaging for IRSpeedyVPN, use the resulting ThroneCore binary under the
existing V-Guard/SGuard64.exe name (and corresponding architecture/legacy builds).

The Linux-compatible existing Core/QUIC tests pass. The winipcfg test package is
Windows-only and cannot run on Linux; Windows runtime and full CI packaging are
not validated by the local cross-build.
