# Pool probe startup

The eager Xray sidecar used by IRSPEEDY Windows now defers its burst observatory
until `boxmain.Create` has returned successfully. Publishing a newly constructed
box context is insufficient: its DNS transports, network monitor and TUN inbound
have not started yet. An initial probe in that window could record a healthy
member as failed for the Pool's sampling window.

Only the observer scheduler is deferred. Xray listeners and outbounds still
start before sing-box so remote rule-set downloads can use the proxy during
bootstrap. The existing observer is wrapped as an internal typed app config;
its serialized options, selectors, interval, sampling and leastLoad policy are
preserved. The shared observer covers both the main and AI Pools. There is no
new periodic test, sleep, Core restart or UI work.

Before readiness the observer returns its empty observations and ignores forced
checks. A successful box startup releases the normal initial sweep and periodic
scheduler once. Closing an instance before release never launches its probes.
This does not wait for successful probe results or promise two healthy members;
genuine DNS, network, TLS and server failures remain failures.

Scope: eager sidecars in the Start RPC. Standalone tests, lazy/full-config gates,
and profiles without a burst observer retain their existing behavior. AI routing
and fallback rules are not changed.

Generate `gen/libcore.proto` and `gen/xray_hysteria2.proto` with the existing
builder before compiling. `XrayDeferredObservatoryConfig` is internal to Core;
no RPC request fields or Windows configuration changes are required. Rebuild the
Core and package it with the Windows application. Windows' diagnostic reader
also accepts the new `pool-probes-ready` marker (emitted after box startup).

Validation:

```sh
go test -race -tags with_quic ./internal/xray
go test -tags with_quic -run '^$' .
```

The regression test starts a real Xray instance with two main and two AI proxy
members and a deliberately unavailable DNS resolver. No probes may run before
release; after release all four first observations must succeed against a local
HTTP proxy. Separate checks cover exact option preservation, forced-check gating,
idempotent release and closing before release. A real Windows TUN run remains
necessary to validate OS routing and the packaged executable.
