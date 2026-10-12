# net-merge

Use all your network adapters at once on Windows. Every new connection (TCP and UDP, IPv4 and IPv6) is
pinned to one adapter (Wi-Fi, Ethernet, USB tether, phone hotspot), so many parallel connections
(browsers, Steam, updates, downloads) run over all your links at the same time. A single connection is
still limited to one link.

Measured with Wi-Fi + an Android USB tether + an iPhone hotspot, 9 parallel downloads:
**232-303 Mbit/s** vs about 121 Mbit/s for the fastest single link.

No server, VPN or account is involved: connections go straight out of the adapter they are assigned to.

## Uses and Tested on
- Steam downloads
- Large file downloads via IDM
- Large file uploads to Google Drive

## Install

Download `net-merge-setup-x.y.z.exe` from the [releases page](../../releases) and run it (needs admin).
It is not code-signed yet, so SmartScreen will warn: *More info > Run anyway*. A portable zip is also
published.

Then start **net-merge** from the Start menu. A tray icon appears:

- **left-click** toggles ON / OFF
- **right-click** shows live speed per adapter, "Turn on/off", and Quit

Tick *Start net-merge when I sign in* in the installer (or run `net-merge autostart on`) to start it at
sign-in without a UAC prompt.

## Command line

```
net-merge on | off | quit | status | list | autostart on|off|status | version
net-merge tray            tray app (asks for admin once)
net-merge run             system-wide in a console window
net-merge socks           plain SOCKS5 proxy on 127.0.0.1:1080 (no admin, per-app)
net-merge cleanup         remove leftover routes after a crash
```

Flags for `tray`/`run`/`socks`: `-include "WiFi,Ethernet"` (only these adapters), `-weight "Name=3"`
(fixed weight instead of measured), `-ipv6 auto|on|off`, `-log file`.

## How it works

- ON creates a Wintun adapter `net-merge` and two `/1` routes per IP family that beat the default route
  without touching it. OFF deletes them. If the process dies the adapter and routes die with it, so a
  crash can't leave the machine without internet.
- Flows entering the TUN are terminated in a userland TCP/IP stack (tun2socks/gVisor) and re-dialed from
  a chosen adapter: the socket is bound to that adapter's source IP and forced out of it with
  `IP_UNICAST_IF`, so it can never loop back into the TUN.
- Adapters are detected automatically and re-scanned every 3 s. Only real hardware with a default gateway
  is used: tunnels, VPNs (Tailscale, WireGuard, ...), virtual switches and LAN-only adapters are skipped
  (`net-merge list` shows why).
- Each adapter and each address family is probed for real internet access. A tether with no data or a
  captive-portal Wi-Fi gets no flows; a recovered link rejoins within seconds.
- Flows go to the adapter with the lowest (active flows + 1) / weight. Weights follow measured
  throughput, separately for IPv4 and IPv6 (a link can be fast over native IPv6 and slow over translated
  IPv4).
- IPv6 (`-ipv6 auto|on|off`, tray checkbox): IPv6 flows can only leave through adapters that have IPv6, and
  apps that prefer IPv6 (browsers, OBS, curl) would funnel everything onto them. In `auto` mode IPv6 is
  balanced only when every adapter has working IPv6; otherwise new IPv6 connections are refused at the SYN
  (UDP gets ICMP unreachable), apps fall back to IPv4 instantly, and IPv4 spreads over all adapters.
  Cost: IPv6-only hosts are unreachable while the adapters differ (untick the tray box or use `-ipv6 on`).

## Limits

- One connection = one adapter. A single big download won't speed up; many connections will.
- With adapters that differ in IPv6 support, IPv6-only hosts are unreachable in `auto` mode (see above).
- Sites that tie a login to one IP may see a different IP per connection and ask you to re-verify.
- Needs admin (for the TUN). Windows 10/11, x64 only.

## Build

Needs Go (see `go.mod`) and, for the installer, [Inno Setup 6](https://jrsoftware.org/isinfo.php).

```
go test ./...                       # uses loopback aliases as fake adapters; no second link needed
scripts\build.ps1 -Version 0.1.0    # dist\: net-merge.exe, net-merge-tray.exe, installer, portable zip
```

`wintun.dll` is not in the repo: `scripts\fetch-wintun.ps1` downloads it and verifies its SHA-256 and its
WireGuard LLC signature. To run from source, put it next to the exe:

```
go build -o net-merge.exe .
scripts\fetch-wintun.ps1 -OutDir .
```

Releases are built by GitHub Actions when a `vX.Y.Z` tag is pushed (`.github/workflows/release.yml`).
`go test -run xxx -bench Relay ./...` measures the relay data path (about 3 GB/s over loopback).

Don't benchmark against speed.cloudflare.com repeatedly: it rate-limits the IP (HTTP 429) after a few
large downloads and the numbers become meaningless.

## License

MIT, see `LICENSE`. Third-party components are listed in `THIRD-PARTY-NOTICES.md`.
