package studio

import "strings"

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
}

// chains maps an instrument ID to its processing chain, in signal order.
//
// The raw input is always what gets recorded; these effects exist so the
// player hears a usable tone while tracking. That keeps a clean DI on disk and
// leaves the tone free to change afterwards.
var chains = map[string][]Effect{
	"guitar": {
		{ID: "tuner", Name: "Tuner", Label: "TUN", Plugin: "ReaTune"},
		{ID: "overdrive", Name: "Overdrive", Label: "OD", Plugin: "JS: Distortion"},
		{ID: "amp", Name: "Amp Simulator", Label: "AMP", Plugin: "JS: Amp Model"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
		{ID: "compressor", Name: "Compressor", Label: "COMP", Plugin: "ReaComp"},
	},
	"bass": {
		{ID: "tuner", Name: "Tuner", Label: "TUN", Plugin: "ReaTune"},
		{ID: "compressor", Name: "Compressor", Label: "COMP", Plugin: "ReaComp"},
		{ID: "amp", Name: "Bass Amp", Label: "AMP", Plugin: "JS: Amp Model"},
		{ID: "eq", Name: "EQ", Label: "EQ", Plugin: "ReaEQ"},
	},
	// Microphones, keyboard and drums carry no mandatory processing. They
	// still accept a chain, so adding one later is a change here alone.
	"mic1":     {},
	"mic2":     {},
	"keyboard": {},
	"dtx":      {},
}

// Chain returns an instrument's effect chain in signal order.
func Chain(instrumentID string) []Effect {
	return append([]Effect(nil), chains[normalizeID(instrumentID)]...)
}

// LookupEffect finds an effect within an instrument's chain.
func LookupEffect(instrumentID, effectID string) (Effect, bool) {
	want := normalizeID(effectID)
	for _, e := range chains[normalizeID(instrumentID)] {
		if normalizeID(e.ID) == want || normalizeID(e.Name) == want {
			return e, true
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
