# Gecko over the Xray connected packet adapter

The Hysteria extras v2.12.3 Gecko wrapper always advertises `SyscallConn`,
`SetReadBuffer`, and `SetWriteBuffer`. If its underlying connection does not
support those operations, they return `errors.ErrUnsupported`.

Our Xray bridge is a connected `net.Conn` exposed as `net.PacketConn`, not a raw
UDP socket. quic-go calls `SyscallConn` when present and fails on that error before
sending a handshake. The outer HTTP/SOCKS probe can then report only a closed TCP
connection. This is independent of the URL-test timeout and server credentials.

`wrapXrayGecko` retains Gecko's framing, obfuscation and lifecycle, but exposes
only the `net.PacketConn` interface that this bridge supports. QUIC uses its
generic packet path. No raw socket handle is synthesized and no obfuscation is
bypassed. Salamander and un-obfuscated connections are unchanged.

Focused regression, using the module's pinned dependencies:

```sh
go test -v -timeout 30s internal/xray/hysteria2_gecko.go internal/xray/hysteria2_gecko_test.go
```

The in-memory connection reproduces the upstream pre-handshake failure, then
verifies that the adapter sends handshake datagrams and reaches the test's
deadline while waiting for a nonexistent peer. No real credentials, sockets or
external servers are used. This verifies the local blocker, not a successful
server handshake. Rebuild and package Core, then validate against a Gecko server
from the user's Windows network before declaring the original connection fixed.
