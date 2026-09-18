package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/studio"
)

// readTopology loads a rig from YAML the way a config file supplies it.
func readTopology(t *testing.T, body string) (studio.Topology, error) {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(body)); err != nil {
		t.Fatalf("test fixture is not valid YAML: %v", err)
	}
	return LoadTopology(v)
}

// The rig people will actually write once they have two guitarists: the kind
// is what decides the pedalboard, so it has to survive the file.
func TestKindIsReadFromTheConfigFile(t *testing.T) {
	got, err := readTopology(t, `
studio:
  inputs:
    - {id: guitar, name: Guitar, channel: 1, kind: guitar}
    - {id: guitar2, name: Guitar 2, channel: 2, kind: guitar}
    - {id: sax, name: Sax, channel: 3, kind: line}
  outputs:
    main: [1, 2]
    cues: [[3], [4]]
`)
	if err != nil {
		t.Fatalf("a rig with kinds was refused: %v", err)
	}
	want := []studio.Kind{studio.KindGuitar, studio.KindGuitar, studio.KindLine}
	for i, k := range want {
		if got.Instruments[i].Kind != k {
			t.Errorf("input %d is %q, want %q", i+1, got.Instruments[i].Kind, k)
		}
	}
}

// A file written before kinds existed must keep working untouched: its input
// names already say what they are.
func TestAConfigWithoutKindsStillLoads(t *testing.T) {
	got, err := readTopology(t, `
studio:
  inputs:
    - {id: mic1, name: Mic 1, channel: 1}
    - {id: guitar, name: Guitar, channel: 3}
    - {id: dtx, name: DTX, channel: 7}
  outputs:
    main: [1, 2]
    cues: [[3]]
`)
	if err != nil {
		t.Fatalf("a rig written before kinds was refused: %v", err)
	}
	want := []studio.Kind{studio.KindVocal, studio.KindGuitar, studio.KindDrums}
	for i, k := range want {
		if got.Instruments[i].Kind != k {
			t.Errorf("input %d inferred %q, want %q", i+1, got.Instruments[i].Kind, k)
		}
	}
}

// A misspelled kind is a typo, and silently handing that input a chain nobody
// asked for would be worse than saying so.
func TestAnUnknownKindIsReported(t *testing.T) {
	_, err := readTopology(t, `
studio:
  inputs:
    - {id: horn, name: Horn, channel: 1, kind: trumpet}
  outputs:
    main: [1, 2]
    cues: [[3]]
`)
	if err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if !strings.Contains(err.Error(), "trumpet") {
		t.Errorf("the error does not name the typo: %v", err)
	}
}

// config init writes the rig out to edit, so what it writes has to say what
// each input is — otherwise the first thing a reader wants is missing.
func TestTheWrittenDefaultNamesEveryKind(t *testing.T) {
	f := DefaultTopologyFile()
	for _, in := range f.Inputs {
		if in.Kind == "" {
			t.Errorf("input %q is written without a kind", in.ID)
		}
		if _, err := studio.ParseKind(in.Kind); err != nil {
			t.Errorf("input %q is written as kind %q, which does not parse: %v", in.ID, in.Kind, err)
		}
	}
}
