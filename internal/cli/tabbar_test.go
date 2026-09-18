package cli

import (
	"strings"
	"testing"
)

func eightCuePages() []tab {
	tabs := []tab{{title: consoleTitle}, {title: patchTitle}}
	for i := 1; i <= 8; i++ {
		tabs = append(tabs, tab{title: "CUE " + string(rune('0'+i))})
	}
	return append(tabs, tab{title: "FX"}, tab{title: "MONITOR"})
}

// The full rig is twelve pages, which is more than an 80-column terminal can
// hold on one line. They have to wrap rather than run off the edge.
func TestTabBarWrapsOnANarrowTerminal(t *testing.T) {
	lines := tabLayout(eightCuePages(), 80)
	if len(lines) < 2 {
		t.Fatalf("twelve pages fit on %d line(s) at 80 columns; expected a wrap", len(lines))
	}
	for _, line := range lines {
		width := 0
		for _, i := range line {
			width += len(tabLabel(eightCuePages()[i], i))
		}
		if width > 78 {
			t.Errorf("line of %d columns overflows an 80-column terminal", width)
		}
	}
}

// Every page appears exactly once, in order, however the bar is broken up.
func TestTabBarKeepsEveryPage(t *testing.T) {
	tabs := eightCuePages()
	var got []int
	for _, line := range tabLayout(tabs, 80) {
		got = append(got, line...)
	}
	if len(got) != len(tabs) {
		t.Fatalf("laid out %d pages, have %d", len(got), len(tabs))
	}
	for i, idx := range got {
		if idx != i {
			t.Fatalf("page %d landed at position %d", idx, i)
		}
	}
}

// Digits 1-9 jump to a page, so numbering a tenth would promise a key that
// does nothing.
func TestOnlyReachablePagesAreNumbered(t *testing.T) {
	tabs := eightCuePages()
	for i, tb := range tabs {
		label := tabLabel(tb, i)
		numbered := strings.HasPrefix(strings.TrimLeft(label, " "), string(rune('1'+i)))
		if i < 9 && !numbered {
			t.Errorf("page %d (%s) has no digit", i+1, tb.title)
		}
		if i >= 9 && numbered {
			t.Errorf("page %d (%s) is numbered but no digit reaches it", i+1, tb.title)
		}
	}
}
