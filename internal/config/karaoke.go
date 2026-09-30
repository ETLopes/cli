package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/studio"
)

// KaraokeSection is the config key the karaoke tool's settings live under.
const KaraokeSection = "karaoke"

// Karaoke config keys.
const (
	KeyKaraokeDevice     = KaraokeSection + ".device"
	KeyKaraokeOutputs    = KaraokeSection + ".outputs"
	KeyKaraokeInputs     = KaraokeSection + ".inputs"
	KeyKaraokeDifficulty = KaraokeSection + ".difficulty"
	KeyKaraokeSlackMS    = KaraokeSection + ".slack_ms"
	KeyKaraokeSongsDir   = KaraokeSection + ".songs_dir"
	KeyKaraokeExportDir  = KaraokeSection + ".export_dir"
	KeyKaraokeRecordDir  = KaraokeSection + ".record_dir"
	KeyKaraokePausePrep  = KaraokeSection + ".pause_prep_while_singing"
)

const (
	karaokeDefaultSlackMS  = 100
	karaokeMaxSlackMS      = 1000
	karaokeDefaultDiff     = "medium"
	karaokeDefaultPlayer   = "Player 1"
	karaokeStateDirName    = "karaoke"
	karaokeQueueFileName   = "queue.json"
	karaokeCalibrationsDir = "calibrations"
)

// KaraokeInput is one microphone and the player who sings into it.
type KaraokeInput struct {
	// Channel is the 1-based capture channel.
	Channel int `mapstructure:"channel" yaml:"channel"`
	// Name is what the player is called on screen.
	Name string `mapstructure:"name" yaml:"name"`
}

// Karaoke holds the karaoke tool's settings.
type Karaoke struct {
	// Device is the audio device's name; empty means the system default.
	Device string `mapstructure:"device" yaml:"device"`
	// Outputs is the 1-based output pair that carries the backing track.
	Outputs []int `mapstructure:"outputs" yaml:"outputs"`
	// Inputs are the microphones, one player each.
	Inputs []KaraokeInput `mapstructure:"inputs" yaml:"inputs"`
	// Difficulty is easy, medium or hard.
	Difficulty string `mapstructure:"difficulty" yaml:"difficulty"`
	// SlackMS is the timing tolerance either side of a note, in milliseconds.
	SlackMS int `mapstructure:"slack_ms" yaml:"slack_ms"`
	// SongsDir is the song library.
	SongsDir string `mapstructure:"songs_dir" yaml:"songs_dir"`
	// ExportDir is where UltraStar exports are written.
	ExportDir string `mapstructure:"export_dir" yaml:"export_dir"`
	// RecordDir, when set, receives a replayable recording of each session.
	RecordDir string `mapstructure:"record_dir" yaml:"record_dir"`
	// PausePrepWhileSinging stops new song preparations while one is sung, so
	// the CPU belongs to the audio.
	PausePrepWhileSinging bool `mapstructure:"pause_prep_while_singing" yaml:"pause_prep_while_singing"`
}

// KaraokeDefaults returns the built-in settings for a studio: the karaoke
// plays through the studio's main outputs and listens on its vocal inputs.
func KaraokeDefaults(t studio.Topology) Karaoke {
	outputs := []int{1, 2}
	if t.Main.Output.Left > 0 && !t.Main.Output.Mono() {
		outputs = []int{t.Main.Output.Left, t.Main.Output.Right}
	}
	var inputs []KaraokeInput
	for _, in := range t.Instruments {
		if in.EffectiveKind() == studio.KindVocal {
			inputs = append(inputs, KaraokeInput{Channel: in.Input, Name: in.Name})
		}
	}
	if len(inputs) == 0 {
		inputs = []KaraokeInput{{Channel: 1, Name: karaokeDefaultPlayer}}
	}
	home := homeOr("")
	return Karaoke{
		Outputs:               outputs,
		Inputs:                inputs,
		Difficulty:            karaokeDefaultDiff,
		SlackMS:               karaokeDefaultSlackMS,
		SongsDir:              filepath.Join(home, "Music", "karaoke"),
		ExportDir:             filepath.Join(home, "Music", "karaoke", "UltraStar"),
		PausePrepWhileSinging: true,
	}
}

func homeOr(fallback string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return home
}

// KaraokeStateDir is where the queue and the calibrations live, under the same
// data-dir convention as the studio sessions.
func KaraokeStateDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(homeOr("."), ".local", "share")
	}
	return filepath.Join(base, AppName, karaokeStateDirName)
}

// KaraokeQueuePath is the persisted queue file.
func KaraokeQueuePath() string { return filepath.Join(KaraokeStateDir(), karaokeQueueFileName) }

// KaraokeCalibrationDir is where calibrations are stored.
func KaraokeCalibrationDir() string { return filepath.Join(KaraokeStateDir(), karaokeCalibrationsDir) }

// BindKaraoke registers the karaoke tool's defaults and environment bindings.
//
// The output pair and the inputs get no default here: theirs come from the
// studio's rig, which is only known once it is loaded.
func BindKaraoke(v *viper.Viper) {
	d := KaraokeDefaults(studio.DefaultTopology())
	defaults := map[string]any{
		KeyKaraokeDevice:     d.Device,
		KeyKaraokeDifficulty: d.Difficulty,
		KeyKaraokeSlackMS:    d.SlackMS,
		KeyKaraokeSongsDir:   d.SongsDir,
		KeyKaraokeExportDir:  d.ExportDir,
		KeyKaraokeRecordDir:  d.RecordDir,
		KeyKaraokePausePrep:  d.PausePrepWhileSinging,
	}
	for key, val := range defaults {
		v.SetDefault(key, val)
		_ = v.BindEnv(key)
	}
	_ = v.BindEnv(KeyKaraokeOutputs)
	_ = v.BindEnv(KeyKaraokeInputs)
}

// LoadKaraoke reads the karaoke settings from v, taking the output pair and the
// inputs from the studio topology t when they are not configured.
func LoadKaraoke(v *viper.Viper, t studio.Topology) (Karaoke, error) {
	def := KaraokeDefaults(t)
	cfg := Karaoke{
		Device:                strings.TrimSpace(v.GetString(KeyKaraokeDevice)),
		Outputs:               def.Outputs,
		Inputs:                def.Inputs,
		Difficulty:            strings.ToLower(strings.TrimSpace(v.GetString(KeyKaraokeDifficulty))),
		SlackMS:               v.GetInt(KeyKaraokeSlackMS),
		SongsDir:              expandHome(v.GetString(KeyKaraokeSongsDir)),
		ExportDir:             expandHome(v.GetString(KeyKaraokeExportDir)),
		RecordDir:             expandHome(v.GetString(KeyKaraokeRecordDir)),
		PausePrepWhileSinging: v.GetBool(KeyKaraokePausePrep),
	}

	if raw := v.Get(KeyKaraokeOutputs); raw != nil {
		outs, err := parseIntList(raw)
		if err != nil {
			return Karaoke{}, fmt.Errorf("invalid configuration: %s must be two channel numbers, e.g. [3, 4]: %w",
				KeyKaraokeOutputs, err)
		}
		cfg.Outputs = outs
	}
	if raw := v.Get(KeyKaraokeInputs); raw != nil {
		ins, err := parseInputs(raw)
		if err != nil {
			return Karaoke{}, fmt.Errorf("invalid configuration: %s: %w", KeyKaraokeInputs, err)
		}
		cfg.Inputs = ins
	}

	if err := cfg.Validate(); err != nil {
		return Karaoke{}, err
	}
	return cfg, nil
}

// Validate checks the settings, naming the key a person would edit.
func (k *Karaoke) Validate() error {
	if _, err := score.ParseDifficulty(k.Difficulty); err != nil {
		return fmt.Errorf("invalid configuration: %s must be one of: easy, medium, hard (got %q)",
			KeyKaraokeDifficulty, k.Difficulty)
	}
	if k.SlackMS < 0 || k.SlackMS > karaokeMaxSlackMS {
		return fmt.Errorf("invalid configuration: %s must be between 0 and %d (got %d)",
			KeyKaraokeSlackMS, karaokeMaxSlackMS, k.SlackMS)
	}
	if len(k.Outputs) != 2 || k.Outputs[0] < 1 || k.Outputs[1] < 1 || k.Outputs[0] == k.Outputs[1] {
		return fmt.Errorf("invalid configuration: %s must be two different channels counting from 1, e.g. [3, 4] (got %v)",
			KeyKaraokeOutputs, k.Outputs)
	}
	if len(k.Inputs) == 0 {
		return fmt.Errorf("invalid configuration: %s needs at least one microphone", KeyKaraokeInputs)
	}
	seen := make(map[int]bool, len(k.Inputs))
	for i := range k.Inputs {
		in := &k.Inputs[i]
		if in.Channel < 1 {
			return fmt.Errorf("invalid configuration: %s[%d] needs a channel of 1 or more (got %d)",
				KeyKaraokeInputs, i, in.Channel)
		}
		if seen[in.Channel] {
			return fmt.Errorf("invalid configuration: %s lists channel %d twice; each microphone is one player",
				KeyKaraokeInputs, in.Channel)
		}
		seen[in.Channel] = true
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "Player " + strconv.Itoa(i+1)
		}
	}
	if k.SongsDir == "" {
		return fmt.Errorf("invalid configuration: %s must be set", KeyKaraokeSongsDir)
	}
	if k.ExportDir == "" {
		return fmt.Errorf("invalid configuration: %s must be set", KeyKaraokeExportDir)
	}
	return nil
}

// InputChannels lists the capture channels, in player order.
func (k Karaoke) InputChannels() []int {
	out := make([]int, len(k.Inputs))
	for i, in := range k.Inputs {
		out[i] = in.Channel
	}
	return out
}

// PlayerNames lists the players' names, in input order.
func (k Karaoke) PlayerNames() []string {
	out := make([]string, len(k.Inputs))
	for i, in := range k.Inputs {
		out[i] = in.Name
	}
	return out
}

// OutputPair is the configured pair as an array.
func (k Karaoke) OutputPair() [2]int {
	if len(k.Outputs) != 2 {
		return [2]int{}
	}
	return [2]int{k.Outputs[0], k.Outputs[1]}
}

// parseIntList reads channel numbers from a config file's list or from an
// environment variable's "3,4".
func parseIntList(raw any) ([]int, error) {
	switch r := raw.(type) {
	case string:
		var out []int
		for _, part := range strings.FieldsFunc(r, func(c rune) bool { return c == ',' || c == ' ' || c == '/' }) {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", part)
			}
			out = append(out, n)
		}
		return out, nil
	case []int:
		return r, nil
	case []string:
		return parseIntList(strings.Join(r, ","))
	case []any:
		out := make([]int, 0, len(r))
		for _, item := range r {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			case string:
				m, err := strconv.Atoi(n)
				if err != nil {
					return nil, fmt.Errorf("%q is not a number", n)
				}
				out = append(out, m)
			default:
				return nil, fmt.Errorf("%v is not a number", item)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected value %v", raw)
}

// parseInputs reads the microphones from a config file's list of
// {channel, name} or from an environment variable's "1:Ana,2:Bruno".
func parseInputs(raw any) ([]KaraokeInput, error) {
	if s, ok := raw.(string); ok {
		var out []KaraokeInput
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			ch, name, _ := strings.Cut(part, ":")
			n, err := strconv.Atoi(strings.TrimSpace(ch))
			if err != nil {
				return nil, fmt.Errorf("%q is not a channel number; write inputs as 1:Name,2:Name", ch)
			}
			out = append(out, KaraokeInput{Channel: n, Name: strings.TrimSpace(name)})
		}
		return out, nil
	}
	tmp := viper.New()
	tmp.Set("inputs", raw)
	var out []KaraokeInput
	if err := tmp.UnmarshalKey("inputs", &out); err != nil {
		return nil, fmt.Errorf("each input needs a channel and a name: %w", err)
	}
	return out, nil
}

// ParseChannels reads a typed channel list such as "3,4", for the settings
// editor.
func ParseChannels(s string) ([]int, error) { return parseIntList(s) }
