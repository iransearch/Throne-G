# Probe DoH transport

`https-race` composes 2–4 verified HTTPS transports under one DNS tag. IRSPEEDY
uses it only in throwaway URL-test profiles as `dns-direct`, with literal-IP
Google, Cloudflare and Yandex endpoints on the physical interface. Existing live
DNS profiles, Xray resolver strategy, RPC fields and sing-box pin are unchanged.

This must be a transport: Xray server resolution and native outbound dialers
explicitly pin `dns-direct`, bypassing DNS routing rules. Both `Create` and
`Check` register the type before parsing. Old Core binaries reject this type;
ship the updated Core together with the app's new probe profile.

All child exchanges start concurrently. A matching, non-truncated NOERROR
(including NODATA) or NXDOMAIN wins. Errors/SERVFAIL/REFUSED do not. The overall
limit is 4s or earlier parent cancellation. Canceling losers does not wait for
them. There is no system/local fallback. Existing HTTPS transports own socket
routing, TLS/SNI/certificate verification, connection pooling and reset behavior.

Validation:

```
go test -race ./internal/probedns ./internal/boxmain
go test -race ./internal/xraydns -run 'Test(LookupBefore|LookupWithNoFamily|StrategyFor)'
```

Tests cover all three winners, sync/async return while canceled losers are still
blocked, valid negatives, invalid responses, cancellation/all-failed, production
HTTPS/TLS wire exchanges and native/Xray pinned lookups with another provider
failing. Set `IRSPEEDY_PROBE_DNS_FIXTURE` to an exported app probe JSON to validate
the exact profile and native Shadowsocks constructors using `boxmain.Check`.
The two existing full-box `internal/xraydns` tests need permission to create
Linux netlink sockets; they cannot run in this restricted container. Controlled
local tests do not replace packaged Windows testing or public DoH reachability.
