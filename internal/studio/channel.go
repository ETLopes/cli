package studio

import (
	"fmt"
	"math"
)

// A channel strip is what a mixing desk puts under each input: gain, tone,
// dynamics, placement and level. It is modelled separately from the pedalboard
// because it is a different thing -- always present, always in the same order,
// and reached by muscle memory rather than by name.

// EQ gain bounds. The plugin reaches +12 dB at the top of its range, and a
// desk's tone controls are symmetrical, so the cut matches the boost.
const (
	MaxEQGain Level = 12
	MinEQGain Level = -12
)

// Trim bounds. This is digital input gain rather than the preamp knob on the
// interface, which is analogue and out of reach from here.
const (
	MaxTrim Level = 24
	MinTrim Level = -24
)

// Fader bounds. A desk fader runs from silence to a little above unity.
const (
	MaxFader Level = 10
	MinFader Level = MinLevel
)

// Pan runs from hard left to hard right.
const (
	PanLeft  = -1.0
	PanRight = 1.0
)

// EQ is a three-band tone control, as a desk lays it out.
type EQ struct {
	// Low, Mid and High are gains in decibels.
	Low, Mid, High Level
	// MidFreq is the centre frequency of the sweepable mid, in hertz.
	MidFreq float64
}

// DefaultMidFreq is where the mid sweep starts, in the range that decides
// whether a voice or a guitar sounds boxy.
const DefaultMidFreq = 1000

// Mid sweep bounds.
const (
	MinMidFreq = 200
	MaxMidFreq = 8000
)

// Flat reports whether the EQ is doing nothing.
func (e EQ) Flat() bool { return e.Low == 0 && e.Mid == 0 && e.High == 0 }

// Channel is one input's strip.
type Channel struct {
	// Trim is digital input gain, applied before everything else.
	Trim Level
	// EQ is the three-band tone control.
	EQ EQ
	// Comp is compression amount, 0 to 1. Zero leaves the compressor idle;
	// higher values lower its threshold so it works harder.
	Comp float64
	// Pan places the channel in the stereo field, -1 hard left to +1 hard
	// right.
	Pan float64
	// Fader is the channel's level in the main mix.
	Fader Level
	// Muted silences the channel everywhere.
	Muted bool
	// Soloed silences every channel that is not soloed.
	Soloed bool
}

// NewChannel returns a strip at its neutral position: everything flat, centred
// and at unity, which is where a desk sits before anyone touches it.
func NewChannel() Channel {
	return Channel{
		EQ:    EQ{MidFreq: DefaultMidFreq},
		Fader: Unity,
	}
}

// CompThreshold converts a compression amount into the threshold that produces
// it. Amount is the useful control: a player asks for more compression, not for
// a threshold of minus eighteen decibels.
//
// Zero maps to 0 dBFS, where nothing crosses the threshold and the compressor
// is idle. Full maps to -40 dB, which is working hard on any normal signal.
func (c Channel) CompThreshold() Level {
	if c.Comp <= 0 {
		return 0
	}
	amount := math.Min(c.Comp, 1)
	return Level(-40 * amount)
}

// PanLabel renders pan the way a desk prints it.
func (c Channel) PanLabel() string {
	switch {
	case math.Abs(c.Pan) < 0.005:
		return "C"
	case c.Pan < 0:
		return fmt.Sprintf("L%d", int(math.Round(-c.Pan*100)))
	default:
		return fmt.Sprintf("R%d", int(math.Round(c.Pan*100)))
	}
}

// Validate checks a strip, so a hand-edited session cannot ask for a setting
// the desk does not have.
func (c Channel) Validate() error {
	if c.Trim < MinTrim || c.Trim > MaxTrim {
		return &RangeError{What: "trim", Value: c.Trim, Min: MinTrim, Max: MaxTrim}
	}
	for name, g := range map[string]Level{"low": c.EQ.Low, "mid": c.EQ.Mid, "high": c.EQ.High} {
		if g < MinEQGain || g > MaxEQGain {
			return &RangeError{What: "eq " + name, Value: g, Min: MinEQGain, Max: MaxEQGain}
		}
	}
	if c.EQ.MidFreq != 0 && (c.EQ.MidFreq < MinMidFreq || c.EQ.MidFreq > MaxMidFreq) {
		return fmt.Errorf("eq mid frequency %.0f Hz is out of range (%d to %d)",
			c.EQ.MidFreq, MinMidFreq, MaxMidFreq)
	}
	if c.Comp < 0 || c.Comp > 1 {
		return fmt.Errorf("compression amount %.2f is out of range (0 to 1)", c.Comp)
	}
	if c.Pan < PanLeft || c.Pan > PanRight {
		return fmt.Errorf("pan %.2f is out of range (-1 to 1)", c.Pan)
	}
	if c.Fader < MinFader || c.Fader > MaxFader {
		return &RangeError{What: "fader", Value: c.Fader, Min: MinFader, Max: MaxFader}
	}
	return nil
}

// StripControl names one control on a strip, so the console can step through
// them in the order they appear on a desk.
type StripControl string

// The controls, top to bottom.
const (
	ControlTrim    StripControl = "trim"
	ControlHigh    StripControl = "high"
	ControlMid     StripControl = "mid"
	ControlMidFreq StripControl = "midfreq"
	ControlLow     StripControl = "low"
	ControlComp    StripControl = "comp"
	ControlPan     StripControl = "pan"
	ControlMute    StripControl = "mute"
	ControlSolo    StripControl = "solo"
	ControlFader   StripControl = "fader"
)

// stripOrder is the top-to-bottom order of a strip. Aux sends are inserted
// after comp by the console, since how many there are depends on the rig.
var stripOrder = []StripControl{
	ControlTrim, ControlHigh, ControlMid, ControlMidFreq, ControlLow,
	ControlComp, ControlPan, ControlMute, ControlSolo, ControlFader,
}

// StripControls returns the controls in the order a desk lays them out.
func StripControls() []StripControl { return append([]StripControl(nil), stripOrder...) }

// Label is the short name printed beside the control.
func (c StripControl) Label() string {
	switch c {
	case ControlTrim:
		return "TRIM"
	case ControlHigh:
		return "HIGH"
	case ControlMid:
		return "MID"
	case ControlMidFreq:
		return "FREQ"
	case ControlLow:
		return "LOW"
	case ControlComp:
		return "COMP"
	case ControlPan:
		return "PAN"
	case ControlMute:
		return "MUTE"
	case ControlSolo:
		return "SOLO"
	case ControlFader:
		return "FADER"
	}
	return string(c)
}

// IsSwitch reports whether the control is a switch rather than a continuous
// one, which decides whether it responds to left and right or to space.
func (c StripControl) IsSwitch() bool { return c == ControlMute || c == ControlSolo }
