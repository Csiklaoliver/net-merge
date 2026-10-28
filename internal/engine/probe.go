package engine

import (
	"net"
	"time"

	"net-merge/internal/nics"
)

// Internet reachability probes: a TCP connect to a well-known anycast IP, bound to the NIC.
var probeTargets = [2][]string{
	Fam4: {"1.1.1.1:443", "8.8.8.8:443"},
	Fam6: {"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"},
}

const (
	probeTimeout   = 2 * time.Second
	probeEveryUp   = 15 * time.Second
	probeEveryDown = 4 * time.Second
)

func probe(sl *Slot, f Family) bool {
	return probeAddr(sl.addr.Load(), f)
}

func probeAddr(a *slotAddr, f Family) bool {
	ip := a.ip(f)
	if ip == nil {
		return false
	}
	network := "tcp4"
	if f == Fam6 {
		network = "tcp6"
	}
	for _, t := range probeTargets[f] {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}, Control: nics.ForceEgress(a.index, f == Fam6), Timeout: probeTimeout}
		if c, err := d.Dial(network, t); err == nil {
			c.Close()
			return true
		}
	}
	return false
}

type probeJob struct {
	sl   *Slot
	f    Family
	addr *slotAddr
}

// Ignore results for an adapter that changed while this probe was in flight. In particular,
// a failed check of the old DHCP address must not sideline the newly connected link.
func (s *Scheduler) finishProbe(j probeJob, up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.sl.probing[j.f].Store(false)
	current := j.sl.addr.Load()
	if !j.sl.present || current != j.addr {
		// An unaffected family may also share the replaced snapshot. Check it again promptly.
		j.sl.lastProbe[j.f] = time.Time{}
		return
	}
	if up {
		j.sl.state[j.f] = stateUp
		j.sl.downUntil[j.f] = time.Time{}
	} else {
		j.sl.state[j.f] = stateDown
	}
}

// RunProbes checks every adapter's internet access per address family: new ones immediately,
// healthy ones occasionally, dead ones more often so they rejoin quickly. A tether with no data
// or a captive-portal Wi-Fi therefore never receives flows, and a recovered link is used again
// within seconds. Runs until stop is closed (nil = forever).
func (s *Scheduler) RunProbes(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		for _, j := range s.probeDue() {
			go func() {
				s.finishProbe(j, probeAddr(j.addr, j.f))
			}()
		}
	}
}

// probeDue returns the (slot, family) pairs that need a probe now and marks them in flight.
func (s *Scheduler) probeDue() []probeJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var due []probeJob
	for _, sl := range s.slots {
		if !sl.present {
			continue
		}
		for _, f := range []Family{Fam4, Fam6} {
			if sl.ip(f) == nil || sl.probing[f].Load() {
				continue
			}
			every := probeEveryUp
			switch sl.state[f] {
			case stateUnknown:
				every = 0
			case stateDown:
				every = probeEveryDown
			}
			if now.Sub(sl.lastProbe[f]) >= every {
				sl.lastProbe[f] = now
				sl.probing[f].Store(true)
				due = append(due, probeJob{sl: sl, f: f, addr: sl.addr.Load()})
			}
		}
	}
	return due
}
