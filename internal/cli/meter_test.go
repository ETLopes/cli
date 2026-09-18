package cli

import (
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/daw"
)

// The scale exists to be read while turning a knob, so the ordering has to
// hold: louder must never render shorter.
func TestMeterRisesWithLevel(t *testing.T) {
	const width = 20
	prev := -1
	for _, db := range []float64{-150, -60, -50, -40, -30, -24, -18, -12, -6, -3, 0} {
		got := meterFill(db, width)
		if got < prev {
			t.Errorf("%.0f dB fills %d, less than the level below it (%d)", db, got, prev)
		}
		if got < 0 || got > width {
			t.Errorf("%.0f dB fills %d, outside 0..%d", db, got, width)
		}
		prev = got
	}
}

// Silence is empty and clipping is full; anything in between is neither.
func TestMeterEnds(t *testing.T) {
	if got := meterFill(-150, 20); got != 0 {
		t.Errorf("silence fills %d, want 0", got)
	}
	if got := meterFill(0, 20); got != 20 {
		t.Errorf("0 dBFS fills %d, want 20", got)
	}
	// A signal that is present must show as something, however quiet.
	if got := meterFill(-59, 20); got < 1 {
		t.Errorf("a quiet but present signal fills %d, want at least 1", got)
	}
}

// The top of the scale is where gain decisions are made, so it must get more
// room than the bottom.
func TestMeterFavoursTheUsefulRange(t *testing.T) {
	const width = 40
	low := meterFill(-30, width) - meterFill(-45, width)
	high := meterFill(-15, width) - meterFill(-30, width)
	if high <= low {
		t.Errorf("15 dB near the top spans %d columns, no more than the same span lower down (%d)",
			high, low)
	}
}

// The bar is drawn from multi-byte glyphs, so it has to come out the right
// width on screen rather than the right number of bytes.
func TestMeterBarWidth(t *testing.T) {
	for _, db := range []float64{-150, -50, -20, -3} {
		bar := meterBar(daw.Meter{Peak: db}, 12)
		n := strings.Count(bar, "█") + strings.Count(bar, "·")
		if n != 12 {
			t.Errorf("%.0f dB drew %d cells, want 12", db, n)
		}
	}
}

// Reading a meter is only useful if it says what to do, and silence on an
// input is a different problem from a level that is merely low.
func TestMeterAdvice(t *testing.T) {
	for _, tc := range []struct {
		db   float64
		want string
	}{
		{-150, "meter.silent"},
		// An idle input sits near its noise floor; that is silence, not a
		// gain problem, and saying otherwise is advice about nothing.
		{-88, "meter.silent"},
		{-40, "meter.low"},
		{-12, "meter.good"},
		{-3, "meter.hot"},
	} {
		if got := meterAdvice(daw.Meter{Peak: tc.db}); got != tc.want {
			t.Errorf("%.0f dB advises %q, want %q", tc.db, got, tc.want)
		}
	}
}
