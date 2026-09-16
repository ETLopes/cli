// Package studio models a DAW-based home studio in the terms a musician uses:
// instruments, cue mixes, effects and monitoring. Nothing here knows about
// REAPER, track indexes or OSC addresses.
package studio

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Level is an audio gain in decibels.
//
// Routing changes can produce painfully loud sound, so levels are a distinct
// type rather than a bare float: every value entering the model passes through
// validation, and every change is bounded.
type Level float64

// Level bounds. Sends may be boosted a little, because a quiet instrument in
// someone's headphones is a real need, but the ceiling is deliberately low.
const (
	// MinLevel is treated as silence.
	MinLevel Level = -60
	// MaxSendLevel caps how far a cue send may be boosted.
	MaxSendLevel Level = 12
	// MaxMonitorLevel caps the control-room monitors at unity. Boosting the
	// speakers above the mix level is never what someone means, and getting
	// it wrong is unpleasant at best.
	MaxMonitorLevel Level = 0
	// MaxRelativeChange bounds a single relative adjustment. A fat-fingered
	// "+40" should be refused rather than applied.
	MaxRelativeChange Level = 12
	// SafeStartupLevel is where monitoring begins when no level is known,
	// quiet enough to be startling to nobody.
	SafeStartupLevel Level = -20
)

// Unity is no change in gain.
const Unity Level = 0

// String renders a level the way a mixing desk would, signed and to one
// decimal place at most.
func (l Level) String() string {
	if l <= MinLevel {
		return "-inf"
	}
	s := strconv.FormatFloat(float64(l), 'f', -1, 64)
	if l > 0 {
		return "+" + s
	}
	return s
}

// IsSilent reports whether the level is at or below the silence floor.
func (l Level) IsSilent() bool { return l <= MinLevel }

// clamp constrains l to [MinLevel, max].
func (l Level) clamp(max Level) Level {
	if l < MinLevel {
		return MinLevel
	}
	if l > max {
		return max
	}
	return l
}

// RangeError reports a level outside its permitted range.
type RangeError struct {
	What  string
	Value Level
	Min   Level
	Max   Level
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("%s %s dB is out of range (%s to %s dB)",
		e.What, e.Value, e.Min, e.Max)
}

// ValidateSend checks a cue send level.
func ValidateSend(l Level) error {
	if math.IsNaN(float64(l)) || math.IsInf(float64(l), 0) {
		return &RangeError{What: "send level", Value: l, Min: MinLevel, Max: MaxSendLevel}
	}
	if l < MinLevel || l > MaxSendLevel {
		return &RangeError{What: "send level", Value: l, Min: MinLevel, Max: MaxSendLevel}
	}
	return nil
}

// ValidateMonitor checks a monitor level.
func ValidateMonitor(l Level) error {
	if math.IsNaN(float64(l)) || math.IsInf(float64(l), 0) {
		return &RangeError{What: "monitor level", Value: l, Min: MinLevel, Max: MaxMonitorLevel}
	}
	if l < MinLevel || l > MaxMonitorLevel {
		return &RangeError{What: "monitor level", Value: l, Min: MinLevel, Max: MaxMonitorLevel}
	}
	return nil
}

// Adjustment is a level change, either relative to the current value
// ("+3", "-2") or absolute ("0", "-6").
type Adjustment struct {
	Delta    Level
	Relative bool
}

// ParseAdjustment reads a level argument as written on the command line.
//
// A leading sign means "relative to where it is now", which is how a musician
// asks for change: "more guitar" is +3, not an absolute value they would have
// to look up first. Without a sign the value is absolute.
func ParseAdjustment(raw string) (Adjustment, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Adjustment{}, fmt.Errorf("empty level")
	}
	if strings.EqualFold(s, "off") || strings.EqualFold(s, "mute") {
		return Adjustment{Delta: MinLevel}, nil
	}

	relative := s[0] == '+' || s[0] == '-'
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return Adjustment{}, fmt.Errorf("%q is not a level; use a number such as +3, -2 or 0", raw)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Adjustment{}, fmt.Errorf("%q is not a usable level", raw)
	}

	// A bare "-2" is ambiguous: it reads as both "two decibels down" and the
	// absolute level -2 dB. Relative wins, because that is what someone at a
	// desk means, and absolute values below zero are reachable by asking for
	// a larger cut.
	return Adjustment{Delta: Level(v), Relative: relative}, nil
}

// Apply resolves the adjustment against a current level, clamping the result
// to max. It reports an error when the requested change is implausibly large,
// which is nearly always a typo.
func (a Adjustment) Apply(current, max Level) (Level, error) {
	if !a.Relative {
		next := a.Delta
		if next < MinLevel || next > max {
			return current, &RangeError{What: "level", Value: a.Delta, Min: MinLevel, Max: max}
		}
		return next, nil
	}

	if a.Delta > MaxRelativeChange || a.Delta < -MaxRelativeChange {
		return current, fmt.Errorf(
			"change of %s dB is too large in one step (limit ±%s dB); set an absolute level instead",
			a.Delta, MaxRelativeChange)
	}
	return (current + a.Delta).clamp(max), nil
}
