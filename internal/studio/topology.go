package studio

import (
	"fmt"
	"sort"
	"strings"
)

// The topology describes the fixed shape of the studio: which instrument is
// plugged into which input, and which hardware outputs each bus feeds. It is
// the one place that knows about the physical rig, so adding an instrument or
// a cue mix is an edit here rather than a change spread across the codebase.

// ChannelMode is how many hardware channels an instrument occupies.
type ChannelMode int

const (
	// Mono is a single hardware input.
	Mono ChannelMode = 1
	// Stereo is an adjacent input pair. Nothing uses it yet, but the model
	// carries the concept so keyboard and drums can become stereo later
	// without reshaping anything around them.
	Stereo ChannelMode = 2
)

func (c ChannelMode) String() string {
	if c == Stereo {
		return "stereo"
	}
	return "mono"
}

// Instrument is one physical input, and exactly one track in the DAW. Cue
// mixes are built from sends off these tracks, never from copies of them.
type Instrument struct {
	// ID is the short name used in commands, e.g. "guitar".
	ID string
	// Name is the track name in the DAW, and what the TUI displays.
	Name string
	// Input is the hardware input channel, counting from 1.
	Input int
	// Mode is how many channels the instrument occupies.
	Mode ChannelMode
}

// Channels lists the hardware input channels this instrument occupies.
func (i Instrument) Channels() []int {
	if i.Mode == Stereo {
		return []int{i.Input, i.Input + 1}
	}
	return []int{i.Input}
}

// instruments is the canonical input list, in display order.
//
// Keyboard and DTX are deliberately mono for now: they are wired that way, and
// declaring them stereo would silently pull in whatever is on the next input.
var instruments = []Instrument{
	{ID: "mic1", Name: "Mic 1", Input: 1, Mode: Mono},
	{ID: "mic2", Name: "Mic 2", Input: 2, Mode: Mono},
	{ID: "guitar", Name: "Guitar", Input: 3, Mode: Mono},
	{ID: "bass", Name: "Bass", Input: 4, Mode: Mono},
	{ID: "keyboard", Name: "Keyboard", Input: 5, Mode: Mono},
	{ID: "dtx", Name: "DTX", Input: 7, Mode: Mono},
}

// Instruments returns every instrument, in display order.
func Instruments() []Instrument { return append([]Instrument(nil), instruments...) }

// LookupInstrument finds an instrument by ID, case-insensitively. It also
// accepts the display name, so "Mic 1" and "mic1" both work.
func LookupInstrument(id string) (Instrument, bool) {
	want := normalizeID(id)
	for _, in := range instruments {
		if normalizeID(in.ID) == want || normalizeID(in.Name) == want {
			return in, true
		}
	}
	return Instrument{}, false
}

// normalizeID lowercases and strips separators so "Mic 1", "mic-1" and "mic1"
// all resolve to the same instrument.
func normalizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == ' ' || r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// InstrumentIDs lists every instrument ID, for help text and error messages.
func InstrumentIDs() []string {
	out := make([]string, len(instruments))
	for i, in := range instruments {
		out[i] = in.ID
	}
	return out
}

// OutputPair is a stereo hardware output. Outputs are modelled as pairs even
// though the interface exposes them as discrete channels, because every
// destination in this rig is stereo.
type OutputPair struct {
	Left, Right int
}

func (o OutputPair) String() string { return fmt.Sprintf("%d/%d", o.Left, o.Right) }

// Bus is a mix destination: the control-room monitors, or one headphone cue.
type Bus struct {
	// ID is the short name used in commands, e.g. "main" or "cue1".
	ID string
	// Name is the track name in the DAW.
	Name string
	// Output is the hardware output pair this bus feeds.
	Output OutputPair
	// CueID is the cue number, or 0 for the main bus.
	CueID int
}

// IsCue reports whether the bus is a headphone mix.
func (b Bus) IsCue() bool { return b.CueID > 0 }

// MainBus is the control-room mix, feeding the monitors on outputs 1/2. It is
// kept independent of the cue mixes so changing what the room hears never
// changes what a musician hears.
var MainBus = Bus{ID: "main", Name: "MAIN", Output: OutputPair{1, 2}}

// cueBuses are the headphone mixes, each feeding one aux input on the
// headphone amp. More can be added here; nothing else assumes there are four.
var cueBuses = []Bus{
	{ID: "cue1", Name: "CUE 1", Output: OutputPair{3, 4}, CueID: 1},
	{ID: "cue2", Name: "CUE 2", Output: OutputPair{5, 6}, CueID: 2},
	{ID: "cue3", Name: "CUE 3", Output: OutputPair{7, 8}, CueID: 3},
	{ID: "cue4", Name: "CUE 4", Output: OutputPair{9, 10}, CueID: 4},
}

// CueBuses returns every cue bus, ordered by cue number.
func CueBuses() []Bus { return append([]Bus(nil), cueBuses...) }

// CueCount is how many headphone mixes the studio provides.
func CueCount() int { return len(cueBuses) }

// LookupCue finds a cue bus by its number.
func LookupCue(id int) (Bus, bool) {
	for _, b := range cueBuses {
		if b.CueID == id {
			return b, true
		}
	}
	return Bus{}, false
}

// Buses returns the main bus followed by every cue bus.
func Buses() []Bus { return append([]Bus{MainBus}, cueBuses...) }

// Validate checks the topology for the mistakes that would be worst to find
// out about through the speakers: two instruments sharing an input, or two
// buses sharing a hardware output.
//
// It runs as a test rather than at startup, since the topology is compiled in.
func Validate() error {
	usedInputs := map[int]string{}
	for _, in := range instruments {
		if in.Input < 1 {
			return fmt.Errorf("instrument %q has no input channel", in.ID)
		}
		for _, ch := range in.Channels() {
			if prev, taken := usedInputs[ch]; taken {
				return fmt.Errorf("input %d is claimed by both %q and %q", ch, prev, in.ID)
			}
			usedInputs[ch] = in.ID
		}
	}

	usedOutputs := map[int]string{}
	for _, b := range Buses() {
		if b.Output.Left < 1 || b.Output.Right < 1 {
			return fmt.Errorf("bus %q has an incomplete output pair", b.ID)
		}
		for _, ch := range []int{b.Output.Left, b.Output.Right} {
			if prev, taken := usedOutputs[ch]; taken {
				return fmt.Errorf("output %d is claimed by both %q and %q", ch, prev, b.ID)
			}
			usedOutputs[ch] = b.ID
		}
	}

	ids := map[string]bool{}
	for _, in := range instruments {
		if ids[in.ID] {
			return fmt.Errorf("duplicate instrument ID %q", in.ID)
		}
		ids[in.ID] = true
	}

	cues := CueBuses()
	if !sort.SliceIsSorted(cues, func(i, j int) bool { return cues[i].CueID < cues[j].CueID }) {
		return fmt.Errorf("cue buses are not in order")
	}
	return nil
}
