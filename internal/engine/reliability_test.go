package engine

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"net-merge/internal/nics"
)

func TestAutoUsesIdleLinkForParallelFlows(t *testing.T) {
	for _, f := range []Family{Fam4, Fam6} {
		a, b := nic("fast", "127.0.0.2", 1), nic("slow", "127.0.0.3", 1)
		a.IP6, b.IP6 = net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")
		s := NewScheduler([]nics.Nic{a, b})
		s.SetAuto(true)
		fast, slow := s.slots[0], s.slots[1]
		slow.weight[f] = 0.05
		first := s.AcquireFor(f, nil)
		second := s.AcquireFor(f, nil)
		if first != fast || second != slow {
			t.Fatalf("family %d: two parallel flows did not use both links", f)
		}
		// Once both links are occupied, extra flows still follow the capacity weights.
		if third := s.AcquireFor(f, nil); third != fast {
			t.Fatalf("family %d: capacity weighting lost after spreading", f)
		}
	}
}

func TestAutoSpreadingRespectsHealthAndSkip(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("fast", "127.0.0.2", 1), nic("slow", "127.0.0.3", 0.05)})
	s.SetAuto(true)
	fast, slow := s.slots[0], s.slots[1]
	s.SetState(fast, Fam4, true)
	s.SetState(slow, Fam4, false)
	s.Acquire(nil)
	if s.Acquire(nil) != fast {
		t.Fatal("idle but unhealthy link received a parallel flow")
	}
	s.SetState(slow, Fam4, true)
	if s.Acquire(map[string]struct{}{slow.key: {}}) != fast {
		t.Fatal("idle link bypassed retry exclusion")
	}
	s.Fail(slow, Fam4, time.Hour)
	if s.Acquire(nil) != fast {
		t.Fatal("idle link bypassed cooldown")
	}
}

func TestParallelUDPUsesBothLinksAndReleasesOnClose(t *testing.T) {
	needLoopbackAliases(t)
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.SetDeadline(time.Now().Add(5 * time.Second))
	for _, auto := range []bool{false, true} {
		s := NewScheduler([]nics.Nic{nic("fast", "127.0.0.2", 1), nic("slow", "127.0.0.3", 0.5)})
		s.SetAuto(auto)
		b := NewBalancer(s)
		seen := map[string]bool{}
		var sockets []net.PacketConn
		for range 4 {
			pc, err := b.ListenUDP(context.Background(), Fam4)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { pc.Close() })
			sockets = append(sockets, pc)
			if _, err := pc.WriteTo([]byte("ping"), target.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if _, from, err := target.ReadFrom(buf); err != nil {
				t.Fatal(err)
			} else {
				seen[from.(*net.UDPAddr).IP.String()] = true
			}
		}
		if !seen["127.0.0.2"] || !seen["127.0.0.3"] {
			t.Fatalf("auto=%v: UDP used only %v", auto, seen)
		}
		var active int64
		for _, st := range s.Snapshot() {
			active += st.Active
		}
		if active != 4 {
			t.Fatalf("auto=%v: active UDP flows=%d, want 4", auto, active)
		}
		for _, pc := range sockets {
			pc.Close()
			pc.Close() // releasing twice must not underflow counters
		}
		for _, sl := range s.slots {
			if sl.active.Load() != 0 || sl.actF[Fam4].Load() != 0 || len(sl.flows[Fam4]) != 0 {
				t.Fatal("closed UDP socket retained activity or unbalanced counters")
			}
		}
	}
}

func TestUDPActivityWakesIdleFlow(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("loopback", "127.0.0.1", 1)})
	s.SetAuto(true)
	pc, err := NewBalancer(s).ListenUDP(context.Background(), Fam4)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(5 * time.Second))
	s.Tick(flowIdleAfter)
	if s.slots[0].busy(Fam4) != 0 {
		t.Fatal("idle UDP socket kept link busy")
	}
	if _, err := pc.WriteTo([]byte("ping"), pc.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	if n, _, err := pc.ReadFrom(buf); err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("UDP loopback: %q %v", buf[:n], err)
	}
	s.Tick(time.Second)
	st := s.Snapshot()[0]
	if s.slots[0].busy(Fam4) != 1 || st.BytesIn != 4 || st.BytesOut != 4 {
		t.Fatalf("UDP transfer not counted as busy: %+v", st)
	}
}

func TestPinnedHalfCloseKeepsResponseTracked(t *testing.T) {
	needLoopbackAliases(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	responseRead := make(chan struct{})
	defer close(responseRead)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.Copy(io.Discard, c)
		c.Write([]byte("response after EOF"))
		c.(*net.TCPConn).CloseWrite()
		<-responseRead // keep the Windows socket alive until the response is consumed
	}()
	s := NewScheduler([]nics.Nic{nic("A", "127.0.0.2", 1)})
	s.SetAuto(true)
	raw, err := NewBalancer(s).DialPinned(context.Background(), "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*slotConn)
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot()[0].Active != 1 {
		t.Fatal("half-closed request released a still-downloading response")
	}
	data, err := io.ReadAll(c)
	if err != nil || string(data) != "response after EOF" {
		t.Fatalf("response=%q err=%v", data, err)
	}
	s.Tick(time.Second)
	if s.slots[0].busy(Fam4) != 1 {
		t.Fatal("response bytes after half-close no longer affected scheduling")
	}
	c.Close()
	c.Close()
	if s.Snapshot()[0].Active != 0 {
		t.Fatal("Close did not release slot exactly once")
	}
}

func TestRelayHalfCloseKeepsResponseTracked(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	finish := make(chan struct{})
	defer close(finish)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.Copy(io.Discard, c)
		c.Write([]byte("reply\n"))
		<-finish
	}()
	s, proxy := startProxy(t, []nics.Nic{nic("A", "127.0.0.2", 1)})
	c := socksConnect(t, proxy, ln.Addr().(*net.TCPAddr).Port)
	c.Conn.(*net.TCPConn).CloseWrite()
	if reply, err := bufio.NewReader(c.r).ReadString('\n'); err != nil || reply != "reply\n" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if s.Snapshot()[0].Active != 1 {
		t.Fatal("relay released the response direction at the request's EOF")
	}
}

func TestAdapterReconnectRefreshesRouteAndHealth(t *testing.T) {
	n := nic("phone", "10.0.0.2", 1)
	n.Index = 7
	s := NewScheduler([]nics.Nic{n})
	s.SetAuto(true)
	sl := s.Acquire(nil) // keeps the old slot present across unplugging
	defer s.Release(sl)
	s.SetState(sl, Fam4, false)
	s.Fail(sl, Fam4, time.Hour)
	sl.peak[Fam4], sl.weight[Fam4] = 1e6, 0.1
	oldAddr := sl.addr.Load()
	s.Update(nil)
	n.Index = 12 // USB tether reconnect, same DHCP address but a new interface index
	s.Update([]nics.Nic{n})
	if sl.addr.Load().index != 12 || sl.addr.Load() == oldAddr {
		t.Fatal("reconnected adapter kept stale egress interface")
	}
	if sl.state[Fam4] != stateUnknown || !sl.downUntil[Fam4].IsZero() || sl.peak[Fam4] != 0 || sl.weight[Fam4] != 1 {
		t.Fatal("reconnected adapter retained old health or speed")
	}
}

func TestProbeResultCannotOverwriteChangedAdapter(t *testing.T) {
	for _, change := range []string{"address", "index", "reconnect", "unplug"} {
		t.Run(change, func(t *testing.T) {
			n := nic("phone", "10.0.0.2", 1)
			n.Index = 7
			s := NewScheduler([]nics.Nic{n})
			sl := s.Acquire(nil)
			defer s.Release(sl)
			jobs := s.probeDue()
			if len(jobs) != 1 {
				t.Fatalf("want one probe, got %d", len(jobs))
			}
			switch change {
			case "address":
				n.IP = net.ParseIP("10.1.0.2")
			case "index":
				n.Index++
			case "reconnect", "unplug":
				s.Update(nil)
			}
			if change != "unplug" {
				s.Update([]nics.Nic{n})
			}
			s.finishProbe(jobs[0], false)
			if sl.state[Fam4] == stateDown || sl.probing[Fam4].Load() {
				t.Fatal("stale failure sidelined a different adapter configuration")
			}
			if change != "unplug" && len(s.probeDue()) != 1 {
				t.Fatal("fresh adapter was not promptly re-probed")
			}
		})
	}
}

func TestConcurrentRescanAndProbes(t *testing.T) {
	s := NewScheduler([]nics.Nic{nic("phone", "10.0.0.2", 1)})
	s.SetAuto(true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			n := nic("phone", "10.0.0.2", 1)
			n.Index = i % 2
			s.Update([]nics.Nic{n})
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			for _, j := range s.probeDue() {
				s.finishProbe(j, true)
			}
			if sl := s.Acquire(nil); sl != nil {
				_ = sl.addr.Load().index
				s.Release(sl)
			}
			s.Snapshot()
			s.Tick(time.Second)
		}
	}()
	wg.Wait()
}
