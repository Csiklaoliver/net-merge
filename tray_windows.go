package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
)

const trayRows = 4 // adapters shown in the menu; extra rows stay hidden

func cmdTray(args []string) {
	fs := flag.NewFlagSet("tray", flag.ExitOnError)
	var c common
	c.register(fs)
	hidden := fs.Bool("hidden", true, "hide the console window of the elevated process")
	startOff := fs.Bool("off", false, "start with system-wide mode OFF (default: ON)")
	fs.Parse(args)

	if !isAdmin() {
		child := append([]string{"tray"}, args...)
		child = append(child, "-state-dir", c.stateDir)
		if err := relaunchElevated(child, *hidden); err != nil {
			die(fmt.Errorf("could not start elevated (UAC declined?): %w", err))
		}
		fmt.Println("Approve the UAC prompt. Look for the net-merge icon in the system tray (^ overflow arrow).")
		return
	}
	if !singleInstance() {
		fmt.Println("net-merge is already running.")
		return
	}
	if c.logFile == "" {
		c.stats = 0 // nobody is watching a hidden console
		if !hasConsole() {
			c.logFile = filepath.Join(c.stateDir, "net-merge.log") // windowless build: keep a log for diagnosis
		}
	}
	if err := c.setup(); err != nil {
		die(err)
	}
	if err := checkWintun(); err != nil {
		die(err)
	}

	tun := NewTun(nil)
	mode := func() string {
		if tun.IsOn() {
			return "system-wide (tray)"
		}
		return ""
	}
	sched, bal, err := c.boot(mode)
	if err != nil {
		die(err)
	}
	tun.b = bal

	ui := &trayUI{tun: tun, sched: sched, bal: bal, stateDir: c.stateDir, startOn: !*startOff,
		iconOn: trayIcon(true), iconOff: trayIcon(false), toggleReq: make(chan struct{}, 1)}
	systray.Run(ui.onReady, func() { tun.Off(); clearStatus(c.stateDir) })
}

func checkWintun() error {
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "wintun.dll")); err != nil {
		return fmt.Errorf("wintun.dll not found next to %s", exe)
	}
	return nil
}

type trayUI struct {
	tun       *Tun
	bal       *Balancer
	sched     *Scheduler
	stateDir  string
	startOn   bool
	iconOn    []byte
	iconOff   []byte
	toggleReq chan struct{}

	header, toggleItem, v6Item *systray.MenuItem
	rows                       []*systray.MenuItem
	lastOn                     *bool
	lastHeader                 string
	lastTip                    string
	lastRows                   [trayRows]string
	lastErr                    string
	busyUntil                  time.Time
}

func (u *trayUI) onReady() {
	systray.SetIcon(u.iconOff)
	systray.SetTooltip("net-merge")
	u.header = systray.AddMenuItem("net-merge", "")
	u.header.Disable()
	systray.AddSeparator()
	for i := 0; i < trayRows; i++ {
		r := systray.AddMenuItem("", "")
		r.Disable()
		r.Hide()
		u.rows = append(u.rows, r)
	}
	systray.AddSeparator()
	u.v6Item = systray.AddMenuItemCheckbox("Use all adapters (IPv6 only if every adapter has it)", "Ticked: apps are pushed to IPv4 so every adapter is used. Unticked: IPv6 is allowed and funnels onto the adapters that have it.", u.bal.V6Mode() == V6Auto)
	u.toggleItem = systray.AddMenuItem("Turn on", "Left-click the tray icon does the same")
	quit := systray.AddMenuItem("Quit", "Turns system-wide mode off and exits")
	systray.SetOnTapped(func() {
		select {
		case u.toggleReq <- struct{}{}:
		default:
		}
	})

	go func() {
		if u.startOn {
			u.turn(true)
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			u.refresh()
			select {
			case <-u.toggleItem.ClickedCh:
				u.turn(!u.tun.IsOn())
			case <-u.toggleReq:
				u.turn(!u.tun.IsOn())
			case <-u.v6Item.ClickedCh:
				if u.v6Item.Checked() {
					u.v6Item.Uncheck()
					u.bal.SetV6Mode(V6On)
				} else {
					u.v6Item.Check()
					u.bal.SetV6Mode(V6Auto)
				}
			case <-quit.ClickedCh:
				systray.Quit()
				return
			case <-tick.C:
				switch { // `net-merge on|off|quit` from a terminal
				case takeMarker(u.stateDir, "quit"):
					systray.Quit()
					return
				case takeMarker(u.stateDir, "off"):
					u.turn(false)
				case takeMarker(u.stateDir, "on"):
					u.turn(true)
				}
			}
		}
	}()
}

func (u *trayUI) turn(on bool) {
	u.lastErr = ""
	if !on {
		u.tun.Off()
		clearStatus(u.stateDir)
		return
	}
	u.setHeader("Turning on...")
	if err := u.tun.On(); err != nil {
		u.lastErr = err.Error()
		logf("error: %v\n", err)
	}
}

func (u *trayUI) setHeader(s string) {
	if s != u.lastHeader {
		u.header.SetTitle(s)
		u.lastHeader = s
	}
}

func (u *trayUI) refresh() {
	on := u.tun.IsOn()
	if u.lastOn == nil || *u.lastOn != on {
		if on {
			systray.SetIcon(u.iconOn)
			u.toggleItem.SetTitle("Turn off")
		} else {
			systray.SetIcon(u.iconOff)
			u.toggleItem.SetTitle("Turn on")
		}
		u.lastOn = &on
	}

	var live []SlotStats
	total := 0.0
	for _, s := range u.sched.Snapshot() {
		if s.Present {
			live = append(live, s)
			total += (s.RateIn + s.RateOut) * 8 / 1e6
		}
	}

	switch {
	case u.lastErr != "":
		u.setHeader("Error: " + shorten(u.lastErr, 70))
	case on:
		u.setHeader(fmt.Sprintf("ON  -  %.0f Mbit/s total", total))
	default:
		u.setHeader("OFF")
	}

	var parts []string
	for i, r := range u.rows {
		if i >= len(live) {
			if u.lastRows[i] != "" {
				r.Hide()
				u.lastRows[i] = ""
			}
			continue
		}
		s := live[i]
		mbit := (s.RateIn + s.RateOut) * 8 / 1e6
		txt := fmt.Sprintf("%s:  %.0f Mbit/s  (%d flows)", s.Name, mbit, s.Active)
		if s.State != "up" && s.State6 != "up" { // neither family verified yet, or no internet
			txt = fmt.Sprintf("%s:  %s", s.Name, s.State)
		}
		if s.State6 == "up" {
			txt += "  +IPv6"
		}
		if txt != u.lastRows[i] {
			r.SetTitle(txt)
			r.Show()
			u.lastRows[i] = txt
		}
		parts = append(parts, fmt.Sprintf("%s %.0f", s.Name, mbit))
	}

	tip := "net-merge OFF"
	if on {
		tip = shorten(fmt.Sprintf("net-merge ON - %.0f Mbit/s\n%s", total, strings.Join(parts, " | ")), 120)
	}
	if tip != u.lastTip {
		systray.SetTooltip(tip)
		u.lastTip = tip
	}
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "â€¦"
}
