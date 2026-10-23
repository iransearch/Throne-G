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
go.mod/go.sum and dependency pins are unchanged. Rebuild both Windows 1.4.34 and
the modern/legacy Throne-G Core payloads.

Debug logs identify network/raw, IP network and IP cache winners by provider tag.
`dns.final` describes the default route and is not a provider-winner report.

Run from core/server:

```sh
go test -race ./internal/dnsengine ./internal/vpndns ./internal/boxmain ./internal/boxbox
```
