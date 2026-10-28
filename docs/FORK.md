# Reliability fork

This fork of [rohit267/net-merge](https://github.com/rohit267/net-merge) focuses on keeping
multiple usable connections involved in parallel transfers.

## Changes

- Automatic scheduling uses an idle eligible link before adding more flows to a busy link.
  Once every link is busy, throughput weights decide the next placement. When all links are idle,
  the faster link remains preferred. Fixed weights, health tiers, cooldowns and retry exclusions
  keep their existing behaviour.
- UDP sockets, including long-running QUIC transfers, hold a load count until they close. Their
  activity is sampled just like TCP, so quiet DNS sockets do not keep a link busy indefinitely.
- A TCP half-close keeps the remaining direction in the load count and activity tracking.
- Reconnects and interface index changes refresh the source-address/egress snapshot and reset
  health and capacity estimates. An old probe cannot mark a replacement configuration down.
- The tray's release link points to this fork.

Using both connections requires multiple application connections and independent internet paths.
A single TCP or QUIC connection stays on one link. Existing connections do not migrate when a
link is unplugged; applications must reconnect. A healthy but much slower link can make an individual
parallel request slower: automatic mode now favours involving that idle link. Use fixed `-weight`
values to retain the original weighted placement policy.

## Build this fork

Use the Go version required by `go.mod`:

```sh
git clone https://github.com/Csiklaoliver/net-merge.git
cd net-merge
go test ./...
go vet ./...
go build -o net-merge ./cmd/net-merge
```

On Windows, name the executable `net-merge.exe` and run `scripts/fetch-wintun.ps1`
to obtain the verified driver. Start `net-merge.exe tray` as administrator for system-wide mode.
`net-merge.exe socks` provides the per-app SOCKS proxy without changing system routes.
The upstream installation commands in the README still install upstream releases.

## Validation

`internal/engine/reliability_test.go` covers both address families' scheduling, health/cooldown/skip
filtering, real loopback UDP sockets on two source addresses, UDP idle activity and repeated Close,
TCP response accounting after half-close, adapter index changes/reconnects and stale probe rejection.
Loopback tests verify placement and accounting; they do not establish bandwidth gains on physical
Wi-Fi, Ethernet or cellular connections. Test your own links with multiple parallel downloads and
`net-merge status` before relying on the build.

Windows validation on 2026-10-28 (Intel Ethernet plus a Samsung USB tether):

- The fork was built as `0.3.6-fork.1` and installed over 0.3.5 with the original executables backed up.
- Eight HTTPS requests through the fork's SOCKS proxy completed successfully using both adapters.
- With the fork running system-wide and IPv6 on `auto`, six parallel 1 MiB HTTPS range downloads
  all completed with HTTP 206. During that interval, relay counters increased by 5,377,108 bytes on
  Ethernet and 1,071,284 bytes on the tether, with no additional link failures. These counters include
  other application traffic during the interval; this is a connectivity check, not a speed benchmark.
- Twelve direct UDP DNS queries through system-wide mode all received successful responses.
- All nine new regression tests passed ten consecutive runs on Windows, and `go vet ./...` passed.
  The Windows suite passed with two existing tests excluded: `TestUDPAssociateRefusedCleanly` and
  `TestHalfCloseStillDeliversReply` also fail in the unmodified upstream checkout on this machine
  with connection-reset errors. They have not been removed or disabled in CI.
- The full suite also passed on Linux under WSL. A race-instrumented run was unavailable because
  the local toolchains had no C compiler configured. macOS and physical reconnects were not tested.
