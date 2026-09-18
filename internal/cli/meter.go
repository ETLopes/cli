package cli

import (
	"fmt"
	"strings"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/ui"
)

// A meter is for setting gain, so it is drawn the way a meter bridge is: a
// scale that gives most of its width to the region worth resolving.
//
// Decibels are not linear in usefulness. Everything between silence and -40 dB
// is "too quiet" and needs no detail, while the fifteen decibels below clipping
// are where the decision actually gets made. A plain linear scale spends most
// of its pixels on the part nobody reads.

// meterFloor is the quietest level the scale shows. Below it a signal is too
// far down to gain-stage from, and the bar reads empty.
const meterFloor = -60

// meterGood is the quietest level that counts as a healthy input, and
// meterHot is where headroom runs out.
const (
	meterGood = -24
	meterHot  = -6
)

// meterFill is how much of a bar of the given width a level fills.
func meterFill(db float64, width int) int {
	if db <= meterFloor {
		return 0
	}
	if db >= 0 {
		return width
	}
	// Square-rooted so the top of the scale gets more room than the bottom.
	frac := (db - meterFloor) / -meterFloor
	scaled := frac * frac
	// A signal that is present should never render as nothing at all.
	n := int(scaled*float64(width) + 0.5)
	if n < 1 {
		n = 1
	}
	if n > width {
		n = width
	}
	return n
}

// meterBar renders one level as a bar, coloured by whether the level is worth
// acting on: too quiet to use, healthy, or close enough to clipping to matter.
func meterBar(m daw.Meter, width int) string {
	if m.Silent() {
		return ui.Muted.Render(strings.Repeat("·", width))
	}
	n := meterFill(m.Peak, width)
	// Built as two pieces rather than sliced: both glyphs are multi-byte, so
	// cutting the string by length would land mid-rune.
	filled := strings.Repeat("█", n)
	rest := ui.Muted.Render(strings.Repeat("·", width-n))

	// Styles rather than the Success/Warning helpers: those prefix a status
	// glyph, which belongs on a message and not inside a bar.
	switch {
	case m.Peak >= meterHot:
		return ui.Err.Render(filled) + rest
	case m.Peak >= meterGood:
		return ui.OK.Render(filled) + rest
	}
	return ui.Warn.Render(filled) + rest
}

// meterLabel is the level as a number, or a dash when there is no signal.
func meterLabel(m daw.Meter) string {
	if m.Silent() {
		return "--"
	}
	return fmt.Sprintf("%.0f", m.Peak)
}

// meterAdvice says what to do about a level, which is the whole point of
// looking at one. Silence on an input that should be carrying something is a
// different problem from a level that is merely low.
func meterAdvice(m daw.Meter) string {
	switch {
	// Below the floor nothing is arriving to judge. Telling someone to raise
	// the gain on an instrument nobody is playing is advice about silence.
	case m.Peak <= meterFloor:
		return "meter.silent"
	case m.Peak >= meterHot:
		return "meter.hot"
	case m.Peak >= meterGood:
		return "meter.good"
	}
	return "meter.low"
}
