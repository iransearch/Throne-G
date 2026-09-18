# Core network diagnostics

This instrumentation observes the existing lifecycle. It does not change retries,
timeouts, interface selection, cancellation, or Hysteria2 reset behavior.

Build this Core with the normal production tags, including `with_quic`. The
Hysteria2 observer is installed in `boxbox.New`, including boxes whose context was
created by upstream `box.Context`, as used by RPC tests. The wrapper embeds the
original concrete outbound and delegates `InterfaceUpdated` and `Close` once.

Structured records go to stderr with `[CoreDiagnostic] schema=core-network-v2`.
The bounded asynchronous queue holds 2048 records; it drops records rather than
waiting on a blocked stderr writer. `dropped` is a cumulative count. Tail records
can be lost when the process exits. Ordinary upstream output is unchanged.

Each record contains the PID, process-local sequence number, monotonic elapsed
milliseconds, and box identity. Concurrent emission can reorder delivery; use
the IDs and monotonic time together. Box 0 means process-level metadata.

- `hy2-created`, `hy2-reset`, `hy2-close`: outbound identity and tag hash.
- Reset reason `interface-update`: an existing NetworkManager interface callback
  called reset. This alone does not prove a physical connection changed.
- Reset reason `power-event`: an existing Windows suspend/resume path called reset.
- `network-manager-reset` / `other-caller`: the more specific source was not found.
  Source classification uses fixed function-name categories, never raw stacks.
- `box-created`, `box-start`, `box-close`: lifecycle call boundaries, not success
  acknowledgements. Context state distinguishes active, canceled, and deadline.
- `default-interface`: the existing process-wide monitor's numeric index and flags;
  it is distinct from individual boxes' monitor callbacks.
- RPC, test and probe records link cancellation and cleanup to box/test identities.
  `test-return` precedes deferred environment cleanup.

Tag and config identifiers are the first 8 bytes of SHA-256, lowercase hex. They
are correlation fingerprints, not encryption or proof of identity. No raw config,
tag, URL, server address, credentials, interface name, or error message is added
by these records. The Windows reader accepts only the fixed schema and fields.

Rebuild the Core and package it with the matching Windows reader. Updating only
the Windows application cannot produce these Core records. Keep the established
production build tags and ProtoRPC generation steps; a minimal `with_quic` build
is only a compilation check, not a replacement for the distributed binary.

Focused validation from `core/server`:

```sh
go test -tags with_quic ./internal/netdiag ./internal/boxbox ./internal/boxdns ./test_utils
```

The wrapper test exercises an RPC-style box without connecting to a server or
starting network monitors. Windows runtime validation still needs a real test:
reproduce probes with Proxy/TUN, then correlate `hy2-reset` with the same PID,
box, tag and test. Compare reset origins before deciding on any behavioral fix.
