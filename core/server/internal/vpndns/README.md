# Android VPN DNS parity

Reference: SPEEDY-Exclave-Balancer commit
12f470f443f5f5217a4713f87f24469feacd44f9, irspeedy_race.go.added and
ExclaveBalancerConfig.vpnDns().

The app-owned `vpn-dns-race` transport is separate from direct/probe DNS.
Google, Cloudflare and Yandex use verified DoH; 78.186.60.77 uses UDP/53.
Every request and the TCP fallback pass through the same VPN detour.

| Behavior | Implementation matching Android |
| --- | --- |
| Raw forwarding | All four primary requests run concurrently; first valid completion wins |
| Raw cache | Raw A/AAAA requests neither read nor populate the IP cache |
| Raw failure | SERVFAIL with recursion available after every stage fails |
| IP cache | Separate native TTL entries per provider and address family |
| IP preflight | Read every selected DoH/TCP cache, including fallback, before network work |
| Complete cache | All requested families must be present, unexpired and nonzero TTL |
| Multiple cache hits | Configured resolver order, as on Android |
| UDP cache | Consulted inside that provider's IP race worker, not the global preflight |
| Partial IP cache | Positive addresses may complete that provider without further network work |
| Dual-family lookup | One provider race shared by A/AAAA; never independent provider selections |
| Primary deadline | 8 seconds |
| Fallback | One TCP/53 query per requested family to 1.1.1.1, with 4 seconds |
| Invalid IP result | Wait for stage deadline when no usable IP record was produced |
| Valid negatives | NOERROR/NODATA and NXDOMAIN can win without fallback |
| Invalid raw replies | Reject mismatched IDs/questions/opcodes, malformed/truncated replies and server errors |
| Cancellation | Cancel losing work immediately; caller never waits for worker joining |

IP preflight checks both requested families before any launch. During an IP
provider's network phase, a positive family can finish the provider immediately,
as Android's findIPsForDomain does; missing families become empty results in
the shared lookup result. Negative results need complete requested-family records.
No resolver ranking or permanent preferred provider is stored.

`internal/dnsengine` contains the pinned native client/router plus opt-in hooks
for provider-owned caches and raw/IP separation. Native TTL, cache keys, subnet
options and cache bypass are retained per provider. ClearCache reaches these
provider caches. Other DNS transports retain the original native paths.
UDP has no implicit TCP retry and TCP sends no background reuse probes.

Register the transport before parsing in CLI/Check/RPC contexts. Box.New installs
the local DNS router. The existing Core builder compiles these packages normally;
go.mod/go.sum and dependency pins are unchanged. Rebuild both Windows 1.4.35 and
the modern/legacy Throne-G Core payloads.

Structured `CoreDiagnostic` events identify race construction, sampled winners
and final cumulative counters without requiring Debug logging. No queried names,
answers, endpoints, configuration tags or raw errors are included. Events use a
generated `dns` identity, correlated Box ID, provider `index`, `source`
(`raw-network`, `ip-network`, `ip-cache`), stage/result `elapsedMs`, failure class
and cumulative `wins0` ... `wins4`, `cached`, `failures`, and total `count`.
Indices 0..3 are configured primary order, 4 is TCP fallback, -1 is no winner.
Windows' default order is Google, Cloudflare, Yandex, additional UDP resolver.
The first 32 results and every 128th result are emitted; all results update the
counters, and close emits one final summary. Raw result latency covers the
winning stage; IP latency covers preflight and both stages of the logical lookup.
Existing Debug messages are retained.
`dns.final` describes the default route and is not a provider-winner report.

Windows Proxy routing invokes `resolve` through the existing DNS rules before a
VPN outbound, so the default uses this composite while predefined/Shield,
bootstrap and scoped direct DNS retain precedence. The original domain remains
available to AI/domain rules; connection setup uses resolved IP addresses.
Preceding Shield blockers run again after resolution to catch IP-based matches.
TUN traffic, physical probes and internal DNS detours do not enter that new rule.

Run from core/server:

```sh
go test -race ./internal/dnsengine ./internal/vpndns ./internal/boxmain ./internal/boxbox
```
