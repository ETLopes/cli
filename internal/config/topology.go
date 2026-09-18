package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/studio"
)

// Topology config keys.
const (
	KeyInputs  = StudioSection + ".inputs"
	KeyMainOut = StudioSection + ".outputs.main"
	KeyCueOuts = StudioSection + ".outputs.cues"
)

// Input describes one physical input in the config file.
type Input struct {
	// ID is the short name used in commands, e.g. "guitar".
	ID string `mapstructure:"id" yaml:"id"`
	// Name is the track name in the DAW.
	Name string `mapstructure:"name" yaml:"name"`
	// Channel is the hardware input, counting from 1.
	Channel int `mapstructure:"channel" yaml:"channel"`
	// Mode is "mono" or "stereo". Empty means mono, since declaring stereo by
	// accident silently claims the next input as well.
	Mode string `mapstructure:"mode" yaml:"mode,omitempty"`
	// Kind is what is plugged in — vocal, guitar, bass, keys, drums, line —
	// which decides the effect chain the input gets. Empty is inferred from
	// the ID, so a file written before kinds existed still loads.
	Kind string `mapstructure:"kind" yaml:"kind,omitempty"`
}

// Outputs describes where each bus goes.
type Outputs struct {
	// Main is the monitor pair.
	Main []int `mapstructure:"main" yaml:"main"`
	// Cues are the headphone pairs, in order.
	Cues [][]int `mapstructure:"cues" yaml:"cues"`
	// Phones are the interface's headphone jacks, each the pair of line
	// outputs it is wired to. Not an allocation: a jack carries whatever
	// those outputs already hold, so these overlap the cues on purpose.
	Phones [][]int `mapstructure:"phones" yaml:"phones,omitempty"`
}

// TopologyFile is the configurable shape of the studio.
type TopologyFile struct {
	Inputs  []Input `mapstructure:"inputs" yaml:"inputs"`
	Outputs Outputs `mapstructure:"outputs" yaml:"outputs"`
}

// DefaultTopologyFile renders the built-in rig in configuration form, so
// `config init` writes something a person can edit rather than a blank.
func DefaultTopologyFile() TopologyFile {
	t := studio.DefaultTopology()
	f := TopologyFile{}
	for _, in := range t.Instruments {
		mode := ""
		if in.Mode == studio.Stereo {
			mode = "stereo"
		}
		f.Inputs = append(f.Inputs, Input{
			ID: in.ID, Name: in.Name, Channel: in.Input, Mode: mode,
			Kind: string(in.EffectiveKind()),
		})
	}
	f.Outputs.Main = []int{t.Main.Output.Left, t.Main.Output.Right}
	for _, c := range t.Cues {
		f.Outputs.Cues = append(f.Outputs.Cues, c.Output.Channels())
	}
	for _, ph := range t.Phones {
		f.Outputs.Phones = append(f.Outputs.Phones, ph.Channels())
	}
	return f
}

// LoadTopology reads the rig from configuration, falling back to the built-in
// one when nothing is configured.
//
// A partially configured rig is refused rather than merged with the default:
// silently combining someone's four inputs with six they did not ask for would
// route audio to places they never described.
func LoadTopology(v *viper.Viper) (studio.Topology, error) {
	var f TopologyFile
	if err := v.UnmarshalKey(StudioSection, &f); err != nil {
		return studio.Topology{}, fmt.Errorf("reading studio topology: %w", err)
	}
	if len(f.Inputs) == 0 && len(f.Outputs.Cues) == 0 && len(f.Outputs.Main) == 0 {
		return studio.DefaultTopology(), nil
	}

	t, err := f.Topology()
	if err != nil {
		return studio.Topology{}, err
	}
	if err := t.Validate(); err != nil {
		return studio.Topology{}, fmt.Errorf("studio topology: %w", err)
	}
	return t, nil
}

// Topology converts the configured form into the domain's.
func (f TopologyFile) Topology() (studio.Topology, error) {
	def := studio.DefaultTopology()
	t := studio.Topology{Main: def.Main}

	for i, in := range f.Inputs {
		if in.ID == "" {
			return t, fmt.Errorf("studio.inputs[%d] has no id", i)
		}
		if in.Channel < 1 {
			return t, fmt.Errorf("studio.inputs[%d] (%s) needs a channel of 1 or more", i, in.ID)
		}
		mode := studio.Mono
		switch strings.ToLower(strings.TrimSpace(in.Mode)) {
		case "", "mono":
		case "stereo":
			mode = studio.Stereo
		default:
			return t, fmt.Errorf("studio.inputs[%d] (%s): mode must be mono or stereo, not %q",
				i, in.ID, in.Mode)
		}
		name := in.Name
		if name == "" {
			name = in.ID
		}
		// An unset kind is inferred rather than refused: the rig this was
		// built against predates kinds, and its inputs already say what they
		// are. A kind that is set but unknown is a typo worth reporting.
		kind := studio.InferKind(in.ID)
		if s := strings.TrimSpace(in.Kind); s != "" {
			k, err := studio.ParseKind(s)
			if err != nil {
				return t, fmt.Errorf("studio.inputs[%d] (%s): %w", i, in.ID, err)
			}
			kind = k
		}
		t.Instruments = append(t.Instruments, studio.Instrument{
			ID: in.ID, Name: name, Input: in.Channel, Mode: mode, Kind: kind,
		})
	}
	if len(t.Instruments) == 0 {
		t.Instruments = def.Instruments
	}

	if len(f.Outputs.Main) > 0 {
		pair, err := outputPair(f.Outputs.Main, "studio.outputs.main")
		if err != nil {
			return t, err
		}
		t.Main = studio.Bus{ID: "main", Name: "MAIN", Output: pair}
	}

	// The jacks are a property of the interface, so an unconfigured rig keeps
	// the built-in ones rather than losing them along with the cue layout.
	t.Phones = def.Phones
	if len(f.Outputs.Phones) > 0 {
		t.Phones = nil
		for i, raw := range f.Outputs.Phones {
			pair, err := outputPair(raw, fmt.Sprintf("studio.outputs.phones[%d]", i))
			if err != nil {
				return t, err
			}
			t.Phones = append(t.Phones, pair)
		}
	}

	if len(f.Outputs.Cues) == 0 {
		t.Cues = def.Cues
		return t, nil
	}
	for i, raw := range f.Outputs.Cues {
		pair, err := outputPair(raw, fmt.Sprintf("studio.outputs.cues[%d]", i))
		if err != nil {
			return t, err
		}
		t.Cues = append(t.Cues, studio.Bus{
			ID:     fmt.Sprintf("cue%d", i+1),
			Name:   fmt.Sprintf("CUE %d", i+1),
			Output: pair,
			CueID:  i + 1,
		})
	}
	return t, nil
}

// outputPair reads where a bus leaves the interface: one channel for a mono
// destination, two for a stereo one.
//
// Both forms are allowed because both are real. A headphone amplifier taking
// one input per channel wants a single output per musician; monitors want a
// pair.
func outputPair(raw []int, where string) (studio.OutputPair, error) {
	switch len(raw) {
	case 1:
		if raw[0] < 1 {
			return studio.OutputPair{}, fmt.Errorf("%s channels count from 1", where)
		}
		return studio.OutputPair{Left: raw[0]}, nil
	case 2:
		if raw[0] < 1 || raw[1] < 1 {
			return studio.OutputPair{}, fmt.Errorf("%s channels count from 1", where)
		}
		return studio.OutputPair{Left: raw[0], Right: raw[1]}, nil
	}
	return studio.OutputPair{}, fmt.Errorf(
		"%s must be one channel for mono, e.g. [3], or two for stereo, e.g. [3, 4]", where)
}
