package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/studio"
)

func newKaraokeViper(t *testing.T, yamlBody string) *viper.Viper {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if yamlBody != "" {
		if err := os.MkdirAll(Dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(Dir(), FileName+".yaml"), []byte(yamlBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	v := viper.New()
	Bind(v)
	BindKaraoke(v)
	if _, err := Load(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKaraokeDefaultsComeFromTheStudioTopology(t *testing.T) {
	v := newKaraokeViper(t, "")
	k, err := LoadKaraoke(v, studio.DefaultTopology())
	if err != nil {
		t.Fatal(err)
	}
	topo := studio.DefaultTopology()
	if got := k.OutputPair(); got != [2]int{topo.Main.Output.Left, topo.Main.Output.Right} {
		t.Errorf("outputs = %v, want the studio's main pair", got)
	}
	if len(k.Inputs) != 2 || k.Inputs[0] != (KaraokeInput{1, "Mic 1"}) || k.Inputs[1] != (KaraokeInput{2, "Mic 2"}) {
		t.Errorf("inputs = %+v, want the studio's two vocal mics with their names", k.Inputs)
	}
	if k.Difficulty != "medium" || k.SlackMS != 100 || !k.PausePrepWhileSinging || k.RecordDir != "" || k.Device != "" {
		t.Errorf("unexpected scalar defaults: %+v", k)
	}
	if !strings.HasSuffix(k.SongsDir, filepath.Join("Music", "karaoke")) ||
		!strings.HasSuffix(k.ExportDir, filepath.Join("Music", "karaoke", "UltraStar")) {
		t.Errorf("dirs = %q, %q", k.SongsDir, k.ExportDir)
	}
}

func TestKaraokeDefaultsWithoutVocalInputsOrMainPair(t *testing.T) {
	k := KaraokeDefaults(studio.Topology{Instruments: []studio.Instrument{
		{ID: "guitar", Name: "Guitar", Input: 3, Mode: studio.Mono, Kind: studio.KindGuitar},
	}})
	if len(k.Inputs) != 1 || k.Inputs[0] != (KaraokeInput{1, "Player 1"}) {
		t.Errorf("inputs = %+v, want the single default player", k.Inputs)
	}
	if k.Outputs[0] != 1 || k.Outputs[1] != 2 {
		t.Errorf("outputs = %v, want [1 2]", k.Outputs)
	}
}

func TestKaraokeYAMLOverrides(t *testing.T) {
	v := newKaraokeViper(t, `karaoke:
  device: Scarlett 18i20
  outputs: [3, 4]
  inputs:
    - {channel: 2, name: Ana}
    - {channel: 5, name: Bruno}
  difficulty: Hard
  slack_ms: 150
  songs_dir: ~/songs
  record_dir: /tmp/rec
  pause_prep_while_singing: false
`)
	k, err := LoadKaraoke(v, studio.DefaultTopology())
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if k.Device != "Scarlett 18i20" || k.OutputPair() != [2]int{3, 4} || k.Difficulty != "hard" ||
		k.SlackMS != 150 || k.SongsDir != filepath.Join(home, "songs") || k.RecordDir != "/tmp/rec" ||
		k.PausePrepWhileSinging {
		t.Errorf("overrides not applied: %+v", k)
	}
	if len(k.Inputs) != 2 || k.Inputs[0] != (KaraokeInput{2, "Ana"}) || k.Inputs[1] != (KaraokeInput{5, "Bruno"}) {
		t.Errorf("inputs = %+v", k.Inputs)
	}
}

func TestKaraokeEnvOverrides(t *testing.T) {
	v := newKaraokeViper(t, "")
	t.Setenv("CLI_KARAOKE_DIFFICULTY", "easy")
	t.Setenv("CLI_KARAOKE_SLACK_MS", "80")
	t.Setenv("CLI_KARAOKE_OUTPUTS", "5,6")
	t.Setenv("CLI_KARAOKE_INPUTS", "3:Ana, 4")
	t.Setenv("CLI_KARAOKE_PAUSE_PREP_WHILE_SINGING", "false")
	k, err := LoadKaraoke(v, studio.DefaultTopology())
	if err != nil {
		t.Fatal(err)
	}
	if k.Difficulty != "easy" || k.SlackMS != 80 || k.OutputPair() != [2]int{5, 6} || k.PausePrepWhileSinging {
		t.Errorf("env overrides not applied: %+v", k)
	}
	if len(k.Inputs) != 2 || k.Inputs[0] != (KaraokeInput{3, "Ana"}) || k.Inputs[1] != (KaraokeInput{4, "Player 2"}) {
		t.Errorf("inputs = %+v", k.Inputs)
	}
}

func TestKaraokeValidationMessages(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"difficulty":      {"karaoke:\n  difficulty: brutal\n", "karaoke.difficulty must be one of: easy, medium, hard"},
		"duplicate input": {"karaoke:\n  inputs:\n    - {channel: 1, name: A}\n    - {channel: 1, name: B}\n", "channel 1 twice"},
		"one output":      {"karaoke:\n  outputs: [3]\n", "karaoke.outputs must be two different channels"},
		"same output":     {"karaoke:\n  outputs: [3, 3]\n", "karaoke.outputs must be two different channels"},
		"zero output":     {"karaoke:\n  outputs: [0, 2]\n", "karaoke.outputs must be two different channels"},
		"slack":           {"karaoke:\n  slack_ms: -5\n", "karaoke.slack_ms must be between"},
		"zero input":      {"karaoke:\n  inputs:\n    - {channel: 0, name: A}\n", "needs a channel of 1 or more"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := newKaraokeViper(t, c.yaml)
			_, err := LoadKaraoke(v, studio.DefaultTopology())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestKaraokeStateDirHonoursXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if got, want := KaraokeQueuePath(), filepath.Join("/data", "cli", "karaoke", "queue.json"); got != want {
		t.Errorf("queue path = %q, want %q", got, want)
	}
	if got, want := KaraokeCalibrationDir(), filepath.Join("/data", "cli", "karaoke", "calibrations"); got != want {
		t.Errorf("calibration dir = %q, want %q", got, want)
	}
}
