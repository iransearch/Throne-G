# Native DNS client/router

Source: https://github.com/throneproj/sing-box
Commit: f154bec036c97fc998a7714bea9977bdc343780a
License: GPL-3.0-or-later, retained in LICENSE.

The client/router and their private helpers are copied from the exact pinned
dependency. The transport registry, manager and native wire transports remain
aliases of that dependency. No go.mod/go.sum or external builder override is
needed. Box.New installs this router as adapter.DNSRouter for the live core.

Local changes are limited to managed_transport.go, client.go and router.go:

- Optional ManagedTransport hooks delegate raw queries and IP lookups separately.
- A managed transport owns per-provider IP caches; the outer client never caches
  a composite winner or answers a raw query from an IP cache.
- Read-only CachedResponse never sends a request or refreshes stale/TTL=0 entries.
- CacheResponse retains native TTL/key policy for independently completed providers.
- One lookup context shares the provider result across modern A/AAAA rule paths.
- Native cache-disable options and ClearCache reach the provider caches.

Other transports keep the pinned client's existing cache/routing paths. Keep
these hooks when updating the copied files from a newer upstream commit.

Parity reference: Android commit 12f470f443f5f5217a4713f87f24469feacd44f9,
exclave-core/tools/build-core/patches/app/dns/irspeedy_race.go.added.
