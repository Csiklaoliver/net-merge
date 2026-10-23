package tray

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"net-merge/internal/engine"
)

func TestShortenCountsUTF16AndKeepsRunesWhole(t *testing.T) {
	s := "net-merge ON  ↓12 ↑3.4 Mbit/s"
	if got := shorten(s, 127); got != s {
		t.Fatalf("short string changed: %q", got)
	}
	got := shorten(s, 16)
	if n := len(utf16.Encode([]rune(got))); n > 16 {
		t.Fatalf("%q is %d UTF-16 units, want <= 16", got, n)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("bad cut: %q", got)
	}
}

func TestVersionLabel(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"0.3.3", "v0.3.3"},
		{"v0.3.3", "v0.3.3"}, // already prefixed: do not double it
		{"dev", "dev"},
		{"", "dev"}, // a build without -X main.version
	} {
		if got := versionLabel(c.in); got != c.want {
			t.Errorf("versionLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUpdateLabel(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"v0.3.4", "Update to v0.3.4"},
		{"0.3.4", "Update to v0.3.4"}, // GitHub tags start with v, but do not depend on it
	} {
		if got := updateLabel(c.in); got != c.want {
			t.Errorf("updateLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTrayTooltipFitsWindowsLimit(t *testing.T) {
	const mbit = 125_000
	mk := func(name string, down, up float64) engine.SlotStats {
		return engine.SlotStats{Name: name, Present: true, State: "up", LinkIn: down * mbit, LinkOut: up * mbit}
	}
	two := []engine.SlotStats{mk("Ethernet 3", 16, 0.2), mk("Ethernet 4", 79, 7)}
	tip := trayTooltip(stateWord(true), 95*mbit, 7.2*mbit, two)
	for _, want := range []string{"Merging", "Eth 3 ↓16 ↑0.2", "Eth 4 ↓79 ↑7.0", "Total ↓95 ↑7.2 Mbps"} {
		if utf16Len(tip) > tooltipMax || !strings.Contains(tip, want) {
			t.Fatalf("two adapters should fit with full detail (%q), got %d chars:\n%s", want, utf16Len(tip), tip)
		}
	}
	four := append(two, mk("Wi-Fi", 55, 3), mk("Ethernet 7", 12, 1))
	tip = trayTooltip(stateWord(false), 162*mbit, 11.2*mbit, four)
	if utf16Len(tip) > tooltipMax {
		t.Fatalf("tooltip is %d chars, Windows shows only %d:\n%s", utf16Len(tip), tooltipMax+1, tip)
	}
	for _, want := range []string{"Paused", "Eth 3 ↓", "Eth 4 ↓", "WiFi ↓", "Eth 7 ↓", "Total ↓162 ↑11"} {
		if !strings.Contains(tip, want) {
			t.Fatalf("every adapter keeps its download speed and the total is always shown; %q missing:\n%s", want, tip)
		}
	}
}
