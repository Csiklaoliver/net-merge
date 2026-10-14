package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	wun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/device"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"github.com/xjasonlyu/tun2socks/v2/core/option"
	tlog "github.com/xjasonlyu/tun2socks/v2/log"
	M "github.com/xjasonlyu/tun2socks/v2/metadata"
	"github.com/xjasonlyu/tun2socks/v2/proxy/reject"
	"github.com/xjasonlyu/tun2socks/v2/tunnel"
)

const (
	tunName = "net-merge"
	tunIP   = "198.18.0.1"
	tunMask = "255.255.255.0"
	// A ULA on purpose. Windows' source-address policy prefers IPv4 over IPv6 when the only IPv6
	// source on the route is a ULA, so dual-stack sites go over IPv4 and spread across every
	// adapter, while IPv6-only sites (and explicit IPv6) still work through the IPv6 path. A
	// global-scope address here makes the OS prefer IPv6 everywhere, which funnels all traffic
	// onto whichever adapters happen to have IPv6. Nothing is ever sent from this address.
	tunIP6 = "fd6e:6d65:7267::1"
)

func init() {
	wun.WintunTunnelType = "net-merge"
	// A fixed GUID keeps one Windows network profile instead of a new "Unidentified network" per run.
	if g, err := windows.GUIDFromString("{3f0d5a7e-9b1c-4e52-8a47-1c6b2d9e5f30}"); err == nil {
		wun.WintunStaticRequestedGUID = &g
	}
}

// tunProxy plugs the Balancer into tun2socks: every TCP/UDP flow that enters the TUN is dialed
// out through a pinned NIC. There is no SOCKS hop in between.
type tunProxy struct{ b *Balancer }

func (p tunProxy) DialContext(ctx context.Context, m *M.Metadata) (net.Conn, error) {
	return p.b.DialPinned(ctx, m.DstIP.Unmap().String(), int(m.DstPort))
}

func (p tunProxy) DialUDP(m *M.Metadata) (net.PacketConn, error) {
	f := fam4
	if d := m.DstIP.Unmap(); d.Is6() {
		f = fam6
	}
	return p.b.ListenUDP(context.Background(), f)
}

// Tun is the system-wide switch: On creates the adapter and routes, Off removes them. Both are
// idempotent. If the process dies, the adapter and its routes die with it.
type Tun struct {
	mu  sync.Mutex
	b   *Balancer
	mtu int // set before On; see defaultMTU
	dev device.Device
	stk *stack.Stack
	idx int
	on  bool
}

// defaultMTU is the TUN's MTU, used both by the userland stack and by Windows. TUN traffic never
// leaves the machine (flows are re-dialed with normal sockets), so it isn't bound by the links'
// 1500. Bigger packets mean far fewer per-packet trips through the userland stack: about 6x fewer
// for bulk TCP at 9000. Not 65535: the TUN library allocates a full-MTU buffer for every inbound
// packet, even a 40-byte ACK.
const defaultMTU = 9000

// udpSessionTimeout ends idle UDP sessions sooner than tun2socks' 60s. Torrent DHT opens a session
// per peer queried, each holding a socket, two goroutines and two 64 KB buffers. 30s stays above the
// ~25s keepalives common in VPN and NAT-traversal protocols.
const udpSessionTimeout = 30 * time.Second

func NewTun(b *Balancer) *Tun { return &Tun{b: b, mtu: defaultMTU} }

func (t *Tun) IsOn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.on
}

func (t *Tun) On() (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.on {
		return nil
	}
	// "error", not "warn": tun2socks warns once per dropped UDP packet ("symmetric NAT ... drop
	// packet"), which a torrent client triggers continuously.
	if lvl, e := tlog.ParseLevel("error"); e == nil {
		tlog.SetLogger(tlog.Must(tlog.NewLeveled(lvl)))
	}
	tunnel.T().SetUDPTimeout(udpSessionTimeout)
	cleanupStale()

	dev, err := tun.Open(tunName, uint32(t.mtu))
	if err != nil {
		return fmt.Errorf("open TUN (needs admin and wintun.dll next to net-merge.exe): %w", err)
	}
	stk, err := core.CreateStack(&core.Config{
		LinkEndpoint:     dev,
		TransportHandler: tunnel.T(),
		Options:          []option.Option{option.WithTCPModerateReceiveBuffer(true)},
	})
	if err != nil {
		dev.Close()
		return err
	}
	installForwarders(stk, t.b.V6Allowed)
	t.dev, t.stk = dev, stk
	defer func() {
		if err != nil {
			t.teardown()
		}
	}()
	tunnel.T().SetProxy(tunProxy{t.b})

	if err = run("netsh", "interface", "ipv4", "set", "address", "name="+tunName, "source=static",
		"address="+tunIP, "mask="+tunMask, "store=active"); err != nil {
		return err
	}
	ifc, err := waitIface(tunName, 5*time.Second)
	if err != nil {
		return err
	}
	t.idx = ifc.Index
	// Windows gives Wintun adapters an MTU of 65535. Match the stack's MTU, so Windows sizes TCP
	// segments to it and fragments bigger UDP datagrams instead of sending packets the stack drops.
	idx := strconv.Itoa(t.idx)
	mtu := "mtu=" + strconv.Itoa(t.mtu)
	if e := run("netsh", "interface", "ipv4", "set", "subinterface", "interface="+idx, mtu, "store=active"); e != nil {
		logf("warning: could not set IPv4 MTU: %v\n", e)
	}
	// Two /1 routes beat any default route (/0) without touching it, so Off() is just deleting these.
	for _, dst := range [][2]string{{"0.0.0.0", "128.0.0.0"}, {"128.0.0.0", "128.0.0.0"}} {
		if err = run("route", "add", dst[0], "mask", dst[1], "0.0.0.0", "metric", "1", "if", strconv.Itoa(t.idx)); err != nil {
			return err
		}
	}
	// IPv6 is best-effort: if it can't be set up, IPv4 still works and IPv6 stays on its normal path.
	if e := run("netsh", "interface", "ipv6", "add", "address", "interface="+idx, "address="+tunIP6+"/64", "store=active"); e != nil {
		logf("warning: IPv6 not captured: %v\n", e)
	} else {
		if e := run("netsh", "interface", "ipv6", "set", "subinterface", "interface="+idx, mtu, "store=active"); e != nil {
			logf("warning: could not set IPv6 MTU: %v\n", e)
		}
		for _, p := range []string{"::/1", "8000::/1"} {
			if e := run("netsh", "interface", "ipv6", "add", "route", "prefix="+p, "interface="+idx, "metric=1", "store=active"); e != nil {
				logf("warning: IPv6 route %s: %v\n", p, e)
			}
		}
	}
	t.on = true
	return nil
}

func (t *Tun) Off() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.teardown()
}

func (t *Tun) teardown() {
	if t.idx != 0 {
		delRoutes(t.idx) // traffic falls straight back to the normal default route
	}
	tunnel.T().SetProxy(&reject.Reject{})
	if t.dev != nil {
		t.dev.Close()
	}
	if t.stk != nil {
		done := make(chan struct{})
		go func() { t.stk.Close(); t.stk.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	t.dev, t.stk, t.idx, t.on = nil, nil, 0, false
}

// cleanupStale removes routes left on a surviving net-merge adapter (e.g. after a crash).
func cleanupStale() {
	if ifc, err := net.InterfaceByName(tunName); err == nil {
		delRoutes(ifc.Index)
	}
}

func delRoutes(idx int) {
	for _, dst := range [][2]string{{"0.0.0.0", "128.0.0.0"}, {"128.0.0.0", "128.0.0.0"}} {
		run("route", "delete", dst[0], "mask", dst[1], "0.0.0.0", "if", strconv.Itoa(idx))
	}
	for _, p := range []string{"::/1", "8000::/1"} {
		run("netsh", "interface", "ipv6", "delete", "route", "prefix="+p, "interface="+strconv.Itoa(idx), "store=active")
	}
}

func waitIface(name string, d time.Duration) (*net.Interface, error) {
	deadline := time.Now().Add(d)
	for {
		ifc, err := net.InterfaceByName(name)
		if err == nil {
			return ifc, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("adapter %q did not appear: %w", name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
