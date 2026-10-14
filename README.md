# net-merge

Use all your network adapters at once on Windows. Every new connection (TCP and UDP, IPv4 and IPv6) is
pinned to one adapter (Wi-Fi, Ethernet, USB tether, phone hotspot), so many parallel connections
(browsers, Steam, updates, downloads) run over all your links at the same time. A single connection is
still limited to one link.

Measured with Wi-Fi + an Android USB tether + an iPhone hotspot, 9 parallel downloads:
**232-303 Mbit/s** vs about 121 Mbit/s for the fastest single link.

No server, VPN or account is involved: connections go straight out of the adapter they are assigned to.

## Screenshots

| Hover the tray icon | Right-click the tray icon |
|---|---|
| ![Tooltip: Merging, speed of each adapter and the total](docs/tray-tooltip.png) | ![Menu: total, each adapter's speed and flows, IPv6 option, Pause merging, Quit](docs/tray-menu.png) |

## Features

- **Combines any adapters**: Wi-Fi, Ethernet, USB tethering, phone hotspots. Plug one in and it is used
  within seconds; unplug it and its connections move to the others. VPN and virtual adapters (Tailscale,
  WireGuard, Hyper-V, WSL) are ignored.
- **System-wide**: every app, TCP and UDP, IPv4 and IPv6. No proxy settings, nothing to configure per app.
- **Balances by real speed**: each link gets new connections in proportion to its measured throughput,
  tracked separately for IPv4 and IPv6. A link with no internet (tether without data, captive-portal
  Wi-Fi) gets nothing until it recovers.
- **Live speeds on hover**: each adapter's download/upload and the total. These are the adapters' real
  traffic from Windows, so apps that bypass net-merge (qBittorrent binds to adapters itself) still count.
- **One click on and off**, and optional start at sign-in without a UAC prompt.
- **Safe to crash**: the virtual adapter and its routes disappear with the process, so a crash or a kill
  never leaves the PC without internet.
- **Light on CPU**: about 0.18 cores per 100 Mbit/s.

## Uses and Tested on
- Steam downloads
- Torrent Downloads
- Large file uploads to Google Drive
- Large file downloads using IDM (If server supports multiple connections)

## Install

Download `net-merge-setup-x.y.z.exe` from the [releases page](../../releases) and run it (needs admin).
It is not code-signed yet, so SmartScreen will warn: *More info > Run anyway*. A portable zip is also
published.

## How to use

1. Start **net-merge** from the Start menu and approve the admin prompt. A tray icon appears (if you
   don't see it, it is behind the `^` arrow next to the clock; drag it onto the taskbar).
2. Connect more links: plug in your phone with USB tethering on, join a second Wi-Fi, plug in Ethernet.
   They are picked up automatically.
3. Use the tray icon:
   - **green** = merging, **grey** = paused
   - **left-click** pauses or starts merging
   - **hover** shows each adapter's speed and the total
   - **right-click** shows the same with full adapter names, how many connections net-merge has put
     on each adapter, the IPv6 option, *Pause merging* / *Start merging*, and *Quit*
4. To start it at sign-in, tick *Start net-merge when I sign in* in the installer, or run
   `net-merge autostart on`.

Tips:
- It helps most when an app opens many connections at once: Steam, browsers, cloud uploads, download
  managers that split files (IDM). One single connection always stays on one adapter.
- Torrent clients already use every adapter on their own. Leave qBittorrent's network interface on
  *Any interface*; its speed still shows in the tray.
- *Use all adapters (IPv6 only if every adapter has it)*: leave it ticked. If only some links have IPv6,
  apps that prefer IPv6 would otherwise pile onto those links. Untick it only if you need an IPv6-only site.
- `net-merge list` shows which adapters are used and why others are skipped; `net-merge status` shows
  live per-adapter numbers.

## Command line

```
net-merge on | off        start / pause merging in the running tray
net-merge quit            pause and exit the tray
net-merge status | list   live numbers / which adapters are used and why
net-merge autostart on|off|status
net-merge tray            tray app (asks for admin once)
net-merge run             system-wide in a console window
net-merge socks           plain SOCKS5 proxy on 127.0.0.1:1080 (no admin, per-app)
net-merge cleanup         remove leftover routes after a crash
net-merge version
```

Flags for `tray`/`run`/`socks`: `-include "WiFi,Ethernet"` (only these adapters), `-weight "Name=3"`
(fixed weight instead of measured), `-ipv6 auto|on|off`, `-mtu 9000` (virtual adapter MTU, see below),
`-pprof 127.0.0.1:6060` (Go profiler, loopback only), `-log file`.

## How it works

- ON creates a Wintun adapter `net-merge` and two `/1` routes per IP family that beat the default route
  without touching it. OFF deletes them. If the process dies the adapter and routes die with it, so a
  crash can't leave the machine without internet.
- Flows entering the TUN are terminated in a userland TCP/IP stack (tun2socks/gVisor) and re-dialed from
  a chosen adapter: the socket is bound to that adapter's source IP and forced out of it with
  `IP_UNICAST_IF`, so it can never loop back into the TUN.
- The virtual adapter runs at MTU 9000 (both Windows and the userland stack). Its traffic never leaves
  the machine, so the links' 1500 doesn't apply, and bigger packets cost far less CPU: measured about
  3x less CPU per Mbit/s than at 1500 (0.18 vs 0.54 cores per 100 Mbit/s). `-mtu` changes it.
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
