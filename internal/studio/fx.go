package studio

import (
	"math"
	"strings"
)

// Effect is one processor in an instrument's chain.
//
// Plugin names the processor the DAW should load. It is a preference rather
// than a guarantee: which plugins are installed varies per machine, so the
// adapter reports a clear error when one is missing instead of silently
// leaving the chain incomplete.
type Effect struct {
	// ID is the short name used in commands, e.g. "overdrive".
	ID string
	// Name is what the TUI displays.
	Name string
	// Label is the compact form shown next to an instrument, e.g. "OD".
	Label string
	// Plugin is the processor to load in the DAW.
	Plugin string
	// DefaultOn is whether the effect starts enabled on a fresh session.
	DefaultOn bool
	// Initial holds parameter values written when the plugin is first added,
	// keyed by the name the plugin reports for them. Several stock plugins
	// load in a state that is deliberately transparent -- a compressor whose
	// threshold sits at 0 dBFS never engages -- so switching one on appears
	// to do nothing at all. These give each effect a starting point that is
	// audibly doing its job, and are applied only on creation so later
	// adjustments are never overwritten.
	Initial []ParamDefault
	// Aliases are other names this effect answers to, so an effect can be
	// asked for by the name people actually use for it rather than the one
	// that describes what it does.
	Aliases []string
	// NeedsIR marks a convolution effect that stays transparent until an
	// impulse response is loaded, so the interface can say so rather than
	// leaving the player wondering why an amp changed nothing.
	NeedsIR bool
	// ShowsUI marks an effect whose whole purpose is its display. A tuner
	// processes nothing audible; enabling it without opening its window
	// accomplishes nothing a player can use, so the window is opened with it
	// and closed again when it is switched off.
	ShowsUI bool
}

// ParamDefault is one plugin parameter value.
type ParamDefault struct {
	// Name is the parameter name the plugin reports.
	Name string
	// Value is the raw parameter value, in whatever scale the plugin uses.
	Value float64
}

// DBScalar converts decibels to the linear scalar several Cockos plugins use
// for level parameters, where 1.0 is 0 dBFS. Verified against ReaComp, which
// reports exactly the decibel value asked for.
func DBScalar(db float64) float64 { return math.Pow(10, db/20) }

// chains maps an instrument ID to its processing chain, in signal order.
//
// Order is the order of a real pedalboard, because that is what makes the
// sound people expect: tuner first so it sees a clean signal, dynamics and
// dirt before the amp, modulation and time effects after it, and the gate
// placed where it can catch the noise the drive created.
//
// Everything starts switched off. A chain this long would otherwise be a wall
// of sound the moment an instrument was plugged in.
var chains = map[string][]Effect{
	"guitar": {
		{ID: "tuner", Name: "Tuner", Label: "TUN", Plugin: "ReaTune", ShowsUI: true},
		{ID: "wah", Name: "Wah", Label: "WAH", Plugin: "JS: Wah-Wah"},
		{ID: "octavedown", Name: "Octave Down", Label: "OCT-", Plugin: "JS: Pitch an Octave Down"},
		{ID: "octaveup", Name: "Octave Up", Label: "OCT+", Plugin: "JS: Pitch an Octave Up"},
		{ID: "compressor", Name: "Compressor", Label: "CMP", Plugin: "ReaComp",
			Initial: []ParamDefault{{Name: "Threshold", Value: DBScalar(-18)}}},
		{ID: "overdrive", Name: "Overdrive", Label: "OD", Plugin: "JS: Distortion"},
		{ID: "fuzz", Name: "Fuzz", Label: "FUZZ", Plugin: "JS: Distortion (Fuzz)"},
		{ID: "saturation", Name: "Saturation", Label: "SAT", Plugin: "JS: Saturation"},
		{ID: "clipper", Name: "Clipper", Label: "CLIP", Plugin: "JS: Soft Clipper/Limiter"},
		// After the dirt: a gate before it would only mute a clean signal,
		// while the hiss worth removing is what the drive adds.
		{ID: "gate", Name: "Noise Gate", Label: "GATE", Plugin: "ReaGate"},
		{ID: "amp", Name: "Amp / Cab", Label: "AMP", Plugin: "JS: Convolution Amp/Cab Modeler", NeedsIR: true},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
		{ID: "chorus", Name: "Chorus", Label: "CHO", Plugin: "JS: Chorus (Stereo)"},
		{ID: "flanger", Name: "Flanger", Label: "FLG", Plugin: "JS: Flanger"},
		{ID: "phaser", Name: "Phaser", Label: "PHA", Plugin: "JS: 4-Tap Phaser"},
		{ID: "tremolo", Name: "Tremolo", Label: "TRM", Plugin: "JS: Tremolo"},
		{ID: "delay", Name: "Delay", Label: "DLY", Plugin: "JS: Delay w/Tempo Length"},
		{ID: "pingpong", Name: "Ping-Pong Delay", Label: "PPD", Plugin: "JS: Delay w/Tempo Ping-Pong"},
		{ID: "reverb", Name: "Reverb", Label: "REV", Plugin: "ReaVerbate"},
	},
	"bass": {
		{ID: "tuner", Name: "Tuner", Label: "TUN", Plugin: "ReaTune", ShowsUI: true},
		{ID: "gate", Name: "Noise Gate", Label: "GATE", Plugin: "ReaGate"},
		{ID: "octavedown", Name: "Octave Down", Label: "OCT-", Plugin: "JS: Pitch an Octave Down"},
		{ID: "compressor", Name: "Compressor", Label: "CMP", Plugin: "ReaComp",
			// Bass is usually played with fingers, so a low threshold and a
			// gentle ratio even it out without audible pumping.
			Initial: []ParamDefault{{Name: "Threshold", Value: DBScalar(-18)}}},
		{ID: "comp1175", Name: "1175 Compressor", Label: "1175", Plugin: "JS: 1175 Compressor"},
		{ID: "comptom", Name: "Major Tom Compressor", Label: "TOM", Plugin: "JS: Major Tom Compressor"},
		{ID: "drive", Name: "Drive", Label: "DRV", Plugin: "JS: Distortion"},
		{ID: "saturation", Name: "Saturation", Label: "SAT", Plugin: "JS: Saturation"},
		{ID: "clipper", Name: "Clipper", Label: "CLIP", Plugin: "JS: Soft Clipper/Limiter"},
		{ID: "amp", Name: "Bass Amp / Cab", Label: "AMP", Plugin: "JS: Convolution Amp/Cab Modeler", NeedsIR: true},
		{ID: "lowboost", Name: "Low Boost", Label: "LOW", Plugin: "JS: Bass Manager/Booster"},
		{ID: "chorus", Name: "Chorus", Label: "CHO", Plugin: "JS: Chorus (Stereo)"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
	},
	// Both microphones get the same chain, so either can take a lead vocal.
	"mic1": vocalChain(),
	"mic2": vocalChain(),
	"keyboard": {
		{ID: "compressor", Name: "Compressor", Label: "CMP", Plugin: "ReaComp",
			Initial: []ParamDefault{{Name: "Threshold", Value: DBScalar(-18)}}},
		{ID: "saturation", Name: "Saturation", Label: "SAT", Plugin: "JS: Saturation"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
		{ID: "chorus", Name: "Chorus", Label: "CHO", Plugin: "JS: Chorus (Stereo)"},
		{ID: "delay", Name: "Delay", Label: "DLY", Plugin: "JS: Delay w/Tempo Length"},
		{ID: "lofidelay", Name: "Lo-Fi Delay", Label: "LOFI", Plugin: "JS: Delay (Lo-Fi)"},
		{ID: "reverb", Name: "Reverb", Label: "REV", Plugin: "ReaVerbate"},
	},
	"dtx": {
		{ID: "gate", Name: "Noise Gate", Label: "GATE", Plugin: "ReaGate"},
		{ID: "compressor", Name: "Drum Compressor", Label: "CMP", Plugin: "JS: Digital Drum Compressor"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
		{ID: "reverb", Name: "Room Reverb", Label: "REV", Plugin: "ReaVerbate"},
	},
}

// vocalChain is shared by both microphones.
//
// Dynamics come before pitch correction: a corrector tracks a steady level far
// better than one that lurches, so compressing first makes the tuning follow
// the voice instead of chasing it.
func vocalChain() []Effect {
	return []Effect{
		{ID: "tuner", Name: "Tuner", Label: "TUN", Plugin: "ReaTune", ShowsUI: true},
		{ID: "gate", Name: "Noise Gate", Label: "GATE", Plugin: "ReaGate"},
		{ID: "compressor", Name: "Compressor", Label: "CMP", Plugin: "ReaComp",
			Initial: []ParamDefault{{Name: "Threshold", Value: DBScalar(-18)}}},
		{ID: "deesser", Name: "De-esser", Label: "DES", Plugin: "JS: De-esser"},
		{ID: "autotune", Name: "Pitch Correction", Label: "AUTO", Plugin: "ReaTune",
			Aliases: []string{"pitchfix"}},
		{ID: "hardtune", Name: "Hard Tune", Label: "HARD", Plugin: "ReaTune",
			Aliases: []string{"t-pain", "tpain", "robot"}},
		{ID: "harmony", Name: "Harmony", Label: "HARM", Plugin: "ReaPitch"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
		{ID: "delay", Name: "Delay", Label: "DLY", Plugin: "JS: Delay w/Tempo Length"},
		{ID: "reverb", Name: "Reverb", Label: "REV", Plugin: "ReaVerbate"},
		{ID: "vocoder", Name: "Vocoder", Label: "VOC", Plugin: "ReaVocode"},
	}
}

// Setup notes for effects REAPER cannot configure from outside. ReaTune
// exposes only Bypass, Wet and Delta as automatable parameters, so its
// correction settings have to be set once in its window; they then persist
// with the project.
const (
	correctionSetup = "open the Correction tab and enable correction; " +
		"pick the song's key and scale"
	hardTuneSetup = "open the Correction tab, set retune speed to 0 ms and " +
		"snap to the song's key: that instant snapping is what makes the " +
		"heavily-quantised vocal effect"
)

// Chain returns an instrument's effect chain in signal order.
func Chain(instrumentID string) []Effect {
	return append([]Effect(nil), chains[normalizeID(instrumentID)]...)
}

// LookupEffect finds an effect within an instrument's chain, by ID, display
// name or alias.
func LookupEffect(instrumentID, effectID string) (Effect, bool) {
	want := normalizeID(effectID)
	for _, e := range chains[normalizeID(instrumentID)] {
		if normalizeID(e.ID) == want || normalizeID(e.Name) == want {
			return e, true
		}
		for _, a := range e.Aliases {
			if normalizeID(a) == want {
				return e, true
			}
		}
	}
	return Effect{}, false
}

// EffectIDs lists the effects available on an instrument, for help and errors.
func EffectIDs(instrumentID string) []string {
	c := chains[normalizeID(instrumentID)]
	out := make([]string, len(c))
	for i, e := range c {
		out[i] = e.ID
	}
	return out
}

// HasChain reports whether an instrument has any processing at all.
func HasChain(instrumentID string) bool {
	return len(chains[normalizeID(instrumentID)]) > 0
}

// InstrumentsWithChains lists the instruments that carry processing, in
// topology order.
func InstrumentsWithChains() []string {
	var out []string
	for _, in := range instruments {
		if HasChain(in.ID) {
			out = append(out, in.ID)
		}
	}
	return out
}

// DescribeChain renders a chain as a signal path, for help text.
func DescribeChain(instrumentID string) string {
	c := chains[normalizeID(instrumentID)]
	if len(c) == 0 {
		return "no processing"
	}
	names := make([]string, len(c))
	for i, e := range c {
		names[i] = e.Name
	}
	return strings.Join(names, " → ")
}
