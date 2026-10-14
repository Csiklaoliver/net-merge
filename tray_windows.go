package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
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
	tun.mtu = c.mtu
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

	// Started at sign-in this process can beat explorer.exe by a fraction of a second. With no
	// taskbar the tray library fails to create the icon and never becomes ready, so wait for it.
	if !waitForTaskbar(3 * time.Minute) {
		logf("warning: taskbar not found after 3 minutes, trying anyway\n")
	}
	ready := make(chan struct{})
	go func() { // safety net: if the icon still can't be created, exit so the scheduled task retries
		select {
		case <-ready:
		case <-time.After(30 * time.Second):
			logf("error: tray icon did not initialise within 30s; exiting so the scheduled task can restart it\n")
			os.Exit(4)
		}
	}()

	ui := &trayUI{tun: tun, sched: sched, bal: bal, stateDir: c.stateDir, startOn: !*startOff,
		iconOn: trayIcon(true), iconOff: trayIcon(false), toggleReq: make(chan struct{}, 1), ready: ready}
	systray.Run(ui.onReady, func() { tun.Off(); clearStatus(c.stateDir) })
}

// waitForTaskbar blocks until the shell's taskbar and notification area exist.
func waitForTaskbar(max time.Duration) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	findWindow, findWindowEx := user32.NewProc("FindWindowW"), user32.NewProc("FindWindowExW")
	taskbar, _ := windows.UTF16PtrFromString("Shell_TrayWnd")
	notify, _ := windows.UTF16PtrFromString("TrayNotifyWnd")
	deadline := time.Now().Add(max)
	for {
		if h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(taskbar)), 0); h != 0 {
			if n, _, _ := findWindowEx.Call(h, 0, uintptr(unsafe.Pointer(notify)), 0); n != 0 {
				time.Sleep(time.Second) // let the notification area finish initialising
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
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
	ready     chan struct{} // closed once the tray icon exists

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
	if u.ready != nil {
		close(u.ready)
	}
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
	u.toggleItem = systray.AddMenuItem("Start merging", "Left-click the tray icon does the same")
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
			u.toggleItem.SetTitle("Pause merging")
		} else {
			systray.SetIcon(u.iconOff)
			u.toggleItem.SetTitle("Start merging")
		}
		u.lastOn = &on
	}

	// Speeds are each adapter's real traffic from Windows' counters, i.e. every app (qBittorrent binds
	// to adapters directly and never passes through net-merge), and they are shown even when paused.
	var live []SlotStats
	var down, up float64
	for _, s := range u.sched.Snapshot() {
		if s.Present {
			live = append(live, s)
			down += s.LinkIn
			up += s.LinkOut
		}
	}
	state := stateWord(on)

	if u.lastErr != "" {
		u.setHeader("Error: " + shorten(u.lastErr, 70))
	} else {
		u.setHeader(fmt.Sprintf("%s  ·  Total ↓%s ↑%s Mbps", state, mbps(down), mbps(up)))
	}

	for i, r := range u.rows {
		if i >= len(live) {
			if u.lastRows[i] != "" {
				r.Hide()
				u.lastRows[i] = ""
			}
			continue
		}
		s := live[i]
		txt := fmt.Sprintf("%s:  ↓%s ↑%s Mbps", s.Name, mbps(s.LinkIn), mbps(s.LinkOut))
		if on {
			txt += fmt.Sprintf("  (%d flows via net-merge)", s.Active)
		}
		if s.State != "up" && s.State6 != "up" { // neither family verified yet, or no internet
			txt += "  - " + s.State
		}
		if s.State6 == "up" {
			txt += "  +IPv6"
		}
		if txt != u.lastRows[i] {
			r.SetTitle(txt)
			r.Show()
			u.lastRows[i] = txt
		}
	}

	// Hover tooltip: one line per adapter, so speeds are visible without opening the menu.
	tip := trayTooltip(state, down, up, live)
	if tip != u.lastTip {
		systray.SetTooltip(tip)
		u.lastTip = tip
	}
}

// Windows 11 shows only the first 64 characters of a tray tooltip (measured: the text stopped mid-
// line at exactly 64), although the API accepts 128. So the hover text drops detail until it fits.
const tooltipMax = 63

// stateWord is how the tray names the two states.
func stateWord(on bool) string {
	if on {
		return "Merging"
	}
	return "Paused"
}

// trayTooltip lists each adapter's speed and a Total line, e.g.
//
//	Merging
//	Eth 3 ↓16 ↑0.2
//	Eth 4 ↓79 ↑7.0
//	Total ↓95 ↑7.2 Mbps
//
// It drops detail (the app name, then per-adapter upload, then the unit) until it fits; every
// adapter keeps its download speed and the total is always shown.
func trayTooltip(state string, down, up float64, live []SlotStats) string {
	build := func(head string, withUp bool, unit string) string {
		lines := []string{head}
		for _, s := range live {
			l := shortName(s.Name) + " ↓" + mbps(s.LinkIn)
			if withUp {
				l += " ↑" + mbps(s.LinkOut)
			}
			if s.State == "no internet" && s.State6 != "up" {
				l += " (no net)"
			}
			lines = append(lines, l)
		}
		lines = append(lines, "Total ↓"+mbps(down)+" ↑"+mbps(up)+unit)
		return strings.Join(lines, "\n")
	}
	leanest := build(state, false, "")
	for _, t := range []string{
		build("net-merge · "+state, true, " Mbps"),
		build(state, true, " Mbps"),
		build(state, false, " Mbps"),
		leanest,
	} {
		if utf16Len(t) <= tooltipMax {
			return t
		}
	}
	return shorten(leanest, tooltipMax)
}

// shortName keeps adapter names short enough for the tooltip ("Ethernet 3" -> "Eth 3").
var shortNamer = strings.NewReplacer("Ethernet", "Eth", "Wi-Fi", "WiFi", "Local Area Connection", "LAN", "Cellular", "Cell")

func shortName(n string) string { return shortNamer.Replace(n) }

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// mbps formats bytes/s as Mbit/s compactly: one decimal below 10, whole numbers above.
func mbps(bytesPerSec float64) string {
	m := bytesPerSec * 8 / 1e6
	if m < 9.95 {
		return fmt.Sprintf("%.1f", m)
	}
	return fmt.Sprintf("%.0f", m)
}

// shorten cuts s to at most n UTF-16 units (what Windows counts), ending in an ellipsis.
func shorten(s string, n int) string {
	if utf16Len(s) <= n {
		return s
	}
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if units+w > n-1 {
			return s[:i] + "…"
		}
		units += w
	}
	return s
}
