package studio

import (
	"fmt"
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

// Topology is the shape of one studio: what is plugged into which input, and
// which hardware outputs each bus drives. It is a value rather than a set of
// constants so a different rig can be described in configuration instead of
// requiring a rebuild.
type Topology struct {
	Instruments []Instrument
	Main        Bus
	Cues        []Bus
}

// defaultInstruments is the rig this was built against, and the fallback when
// configuration says nothing.
//
// Keyboard and DTX are deliberately mono: they are wired that way, and
// declaring them stereo would silently pull in whatever is on the next input.
var defaultInstruments = []Instrument{
	{ID: "mic1", Name: "Mic 1", Input: 1, Mode: Mono},
	{ID: "mic2", Name: "Mic 2", Input: 2, Mode: Mono},
	{ID: "guitar", Name: "Guitar", Input: 3, Mode: Mono},
	{ID: "bass", Name: "Bass", Input: 4, Mode: Mono},
	{ID: "keyboard", Name: "Keyboard", Input: 5, Mode: Mono},
	{ID: "dtx", Name: "DTX", Input: 7, Mode: Mono},
}

// current is the topology in force. Package-level accessors read through it,
// so configuring a different rig is one call at startup rather than threading
// a value through every caller.
var current = DefaultTopology()

// DefaultTopology returns the built-in rig.
func DefaultTopology() Topology {
	return Topology{
		Instruments: append([]Instrument(nil), defaultInstruments...),
		Main:        Bus{ID: "main", Name: "MAIN", Output: OutputPair{1, 2}},
		Cues:        append([]Bus(nil), defaultCues...),
	}
}

// Use installs a topology, after checking it describes a workable studio.
// Rejecting a bad one here matters: two instruments sharing an input, or two
// buses sharing an output, is the kind of mistake discovered through the
// speakers.
func Use(t Topology) error {
	if err := t.Validate(); err != nil {
		return err
	}
	current = t
	return nil
}

// Current returns the topology in force.
func Current() Topology { return current }

// Instruments returns every instrument, in display order.
func Instruments() []Instrument { return append([]Instrument(nil), current.Instruments...) }

// LookupInstrument finds an instrument by ID, case-insensitively. It also
// accepts the display name, so "Mic 1" and "mic1" both work.
func LookupInstrument(id string) (Instrument, bool) {
	want := normalizeID(id)
	for _, in := range current.Instruments {
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
	out := make([]string, len(current.Instruments))
	for i, in := range current.Instruments {
		out[i] = in.ID
	}
	return out
}

// OutputPair is where a bus leaves the interface. It is a pair for a stereo
// destination and a single channel for a mono one, because not every
// destination is stereo: a headphone amplifier with one input per channel
// wants one output per musician, not two.
type OutputPair struct {
	// Left is the channel, or the left of a stereo pair.
	Left int
	// Right is the right channel, or zero for a mono output.
	Right int
}

// Mono reports whether the output is a single channel.
func (o OutputPair) Mono() bool { return o.Right == 0 }

// Channels lists the hardware channels this output occupies.
func (o OutputPair) Channels() []int {
	if o.Mono() {
		return []int{o.Left}
	}
	return []int{o.Left, o.Right}
}

func (o OutputPair) String() string {
	if o.Mono() {
		return fmt.Sprint(o.Left)
	}
	return fmt.Sprintf("%d/%d", o.Left, o.Right)
}

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

// MainBus is the control-room mix, feeding the monitors. It is kept
// independent of the cue mixes so changing what the room hears never changes
// what a musician hears.
func MainBus() Bus { return current.Main }

// defaultCues are the headphone mixes, one per input on the headphone
// amplifier. Each is mono and occupies a single output, because that amplifier
// takes one input per headphone channel: eight outputs means eight musicians
// with genuinely independent mixes, where pairing them would serve four.
//
// Nothing assumes there are eight, or that a cue is mono. A rig with stereo
// cues describes them as pairs and the rest follows.
var defaultCues = func() []Bus {
	var out []Bus
	for i := 0; i < 8; i++ {
		out = append(out, Bus{
			ID:     fmt.Sprintf("cue%d", i+1),
			Name:   fmt.Sprintf("CUE %d", i+1),
			Output: OutputPair{Left: 3 + i},
			CueID:  i + 1,
		})
	}
	return out
}()

// CueBuses returns every cue bus, ordered by cue number.
func CueBuses() []Bus { return append([]Bus(nil), current.Cues...) }

// CueCount is how many headphone mixes the studio provides.
func CueCount() int { return len(current.Cues) }

// LookupCue finds a cue bus by its number.
func LookupCue(id int) (Bus, bool) {
	for _, b := range current.Cues {
		if b.CueID == id {
			return b, true
		}
	}
	return Bus{}, false
}

// Buses returns the main bus followed by every cue bus.
func Buses() []Bus { return append([]Bus{current.Main}, current.Cues...) }

// Validate checks a topology for the mistakes that would be worst to find out
// about through the speakers: two instruments sharing an input, or two buses
// sharing a hardware output.
func (t Topology) Validate() error {
	if len(t.Instruments) == 0 {
		return fmt.Errorf("a studio needs at least one input")
	}
	// The monitors are stereo: a mono control room would be a mistake rather
	// than a choice.
	if t.Main.Output.Left < 1 || t.Main.Output.Right < 1 {
		return fmt.Errorf("the main bus needs a stereo output pair")
	}

	usedInputs := map[int]string{}
	ids := map[string]bool{}
	for _, in := range t.Instruments {
		if in.ID == "" {
			return fmt.Errorf("an instrument has no id")
		}
		if ids[in.ID] {
			return fmt.Errorf("duplicate instrument id %q", in.ID)
		}
		ids[in.ID] = true
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
	busIDs := map[string]bool{}
	for _, b := range append([]Bus{t.Main}, t.Cues...) {
		if busIDs[b.ID] {
			return fmt.Errorf("duplicate bus id %q", b.ID)
		}
		busIDs[b.ID] = true
		if b.Output.Left < 1 {
			return fmt.Errorf("bus %q has no output", b.ID)
		}
		if !b.Output.Mono() && b.Output.Right < 1 {
			return fmt.Errorf("bus %q has an incomplete output pair", b.ID)
		}
		for _, ch := range b.Output.Channels() {
			if prev, taken := usedOutputs[ch]; taken {
				return fmt.Errorf("output %d is claimed by both %q and %q", ch, prev, b.ID)
			}
			usedOutputs[ch] = b.ID
		}
	}

	for i, c := range t.Cues {
		if c.CueID != i+1 {
			return fmt.Errorf("cue buses must be numbered 1..%d in order; found %d at position %d",
				len(t.Cues), c.CueID, i+1)
		}
	}
	return nil
}

// Validate checks the topology currently in force.
func Validate() error { return current.Validate() }
