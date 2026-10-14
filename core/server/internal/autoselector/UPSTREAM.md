# Auto Selector round-robin extension

Adapted from throneproj/sing-box f154bec036c9, protocol/group/autoselector.go
and autoselector_health.go. Distributed under the upstream GPL-3.0 license.
The upstream health ring/ranking is retained. Local SOCKS acceptance does not
clear end-to-end probe failures or dial cooldowns, and tested-dead bridges are
excluded from new dials until a real probe succeeds. Successful round-robin dials
update the selected marker without interrupting established connections.
Status types alias upstream so existing QueryAutoSelectors and AutoSelectorAction RPCs keep working.

The registry adds the explicit auto-selector-round-robin outbound type, so old
Cores reject this configuration instead of silently falling back to rotate.
The original auto-selector type and its existing modes remain upstream.
The extension picks the next usable qualified member for each new dial, retaining independent TCP/UDP cursors under
the existing selector mutex. Cooldown, network support, retries and pinning retain
the original policy. Existing connections are not closed by the rotation.

Keep these source copies aligned when changing the pinned sing-box dependency.

After suspension/default-network changes, the main selector can repair failed
Xray Hysteria2 clients through a callback bound to that profile before Start.
Healthy members, the TUN, DNS and the box process stay intact. Each failed member
gets one reset in a two-minute window after network return; a fresh external
probe is required before failure samples from the old client are invalidated.
The AI selector remains the upstream implementation, with its independent policy.

The explicit main-pool `network-down` / `network-return` actions handle WAN
outages without an OS adapter event. They cancel old probes and jitter, pause
while offline, and coalesce an immediate bounded-concurrency sweep. The first
successful external result is published before slow peers finish. Only failures
within the detected outage are cleared; earlier server failures remain. Each
member is cleaned once in the recovery window, and stale results are rejected.
Failed transports are repaired inside their own bounded probe worker, so a
working replacement does not wait for other dead peers to time out. An initial
confirmed-return sweep may drain beyond the recovery window; ordinary later
rechecks cannot extend that window or repeatedly reset failed members.


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
