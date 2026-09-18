package cli

import (
	"testing"

	"github.com/ETLopes/cli/internal/studio"
)

// Retyping an input is the whole point of the page: the bass player left, a
// second guitarist arrived, and input 4 has to become a guitar with a guitar's
// pedalboard rather than a bass's.
func TestRetypingAnInputRenamesAndRechains(t *testing.T) {
	entries := patchEntries()
	idx := -1
	for i, e := range entries {
		if e.Kind == studio.KindBass {
			idx = i
		}
	}
	if idx < 0 {
		t.Skip("the default rig has no bass to retype")
	}

	setPatchKind(entries, idx, studio.KindGuitar)

	if entries[idx].Kind != studio.KindGuitar {
		t.Fatalf("kind is %q after retyping", entries[idx].Kind)
	}
	if entries[idx].ID == "bass" {
		t.Error("the input kept the bass's identity, so it would keep its effects too")
	}
	if entries[idx].Name != "Guitar 2" {
		t.Errorf("named %q, want Guitar 2 beside the guitar already there", entries[idx].Name)
	}

	// The rig it produces must actually give that input a guitar's chain.
	restore := studio.Current()
	defer func() { _ = studio.Use(restore) }()
	if err := studio.Use(patchTopology(entries)); err != nil {
		t.Fatalf("the retyped rig was refused: %v", err)
	}
	if _, ok := studio.LookupEffect(entries[idx].ID, "overdrive"); !ok {
		t.Error("the new guitar cannot reach an overdrive")
	}
}

// Cycling past a kind to look at it must not cost the instrument its identity:
// its saved levels and effects are keyed by that ID.
func TestCyclingBackRestoresTheInstrument(t *testing.T) {
	entries := patchEntries()
	want := entries[0]

	start := want.Kind
	for range studio.Kinds() {
		setPatchKind(entries, 0, entries[0].Kind.Next())
	}

	if entries[0].Kind != start {
		t.Fatalf("a full cycle ended on %q, not %q", entries[0].Kind, start)
	}
	if entries[0].ID != want.ID || entries[0].Name != want.Name {
		t.Errorf("came back as %s/%s, want %s/%s",
			entries[0].ID, entries[0].Name, want.ID, want.Name)
	}
}

// Adding must land somewhere free, or the new input clashes the moment it
// appears.
func TestAddingUsesAFreeInput(t *testing.T) {
	entries := patchEntries()
	before := len(entries)

	entries, err := addPatchEntry(entries)
	if err != nil {
		t.Fatalf("adding an input failed: %v", err)
	}
	if len(entries) != before+1 {
		t.Fatalf("got %d entries, want %d", len(entries), before+1)
	}
	if got := patchConflict(entries, len(entries)-1); got != "" {
		t.Errorf("the added input clashes with %s", got)
	}
	if err := patchTopology(entries).Validate(); err != nil {
		t.Errorf("the rig with an added input was refused: %v", err)
	}
}

// Enough added inputs and every channel is taken; the page has to say so
// rather than pile them onto one channel.
func TestAddingStopsWhenTheInterfaceIsFull(t *testing.T) {
	var entries []patchEntry
	for c := 1; c <= maxInput; c++ {
		entries = append(entries, patchEntry{
			ID: string(rune('a' + c)), Name: "X", Channel: c, Kind: studio.KindLine,
		})
	}
	if _, err := addPatchEntry(entries); err == nil {
		t.Fatal("added an input to a full interface")
	}
}

// A studio with nothing plugged into it is not a studio, and a topology with
// no inputs is refused downstream — better to say so here.
func TestTheLastInputCannotBeRemoved(t *testing.T) {
	entries := []patchEntry{{ID: "guitar", Name: "Guitar", Channel: 1, Kind: studio.KindGuitar}}
	if _, err := removePatchEntry(entries, 0); err == nil {
		t.Fatal("removed the only input")
	}

	entries = append(entries, patchEntry{ID: "bass", Name: "Bass", Channel: 2, Kind: studio.KindBass})
	got, err := removePatchEntry(entries, 0)
	if err != nil {
		t.Fatalf("removing one of two failed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "bass" {
		t.Errorf("after removing the guitar the rig is %v", got)
	}
}

// Removing must not scribble over the slice the caller still holds, or the
// page would show a row that is half the one below it.
func TestRemovingDoesNotDisturbTheOriginal(t *testing.T) {
	entries := []patchEntry{
		{ID: "a", Name: "A", Channel: 1}, {ID: "b", Name: "B", Channel: 2},
		{ID: "c", Name: "C", Channel: 3},
	}
	if _, err := removePatchEntry(entries, 0); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if entries[i].ID != want {
			t.Errorf("the original entry %d became %q, want %q", i, entries[i].ID, want)
		}
	}
}

// The kind has to reach the topology, since that is what decides the chain.
func TestPatchCarriesTheKind(t *testing.T) {
	entries := patchEntries()
	setPatchKind(entries, 0, studio.KindDrums)

	got := patchTopology(entries)
	if got.Instruments[0].Kind != studio.KindDrums {
		t.Errorf("the topology says %q", got.Instruments[0].Kind)
	}
}

// Every kind needs a label, or the page shows a raw key where a word belongs.
func TestEveryKindHasALabel(t *testing.T) {
	for _, k := range studio.Kinds() {
		label := kindLabel(k)
		if label == "" || label == "kind."+string(k) {
			t.Errorf("kind %q renders as %q", k, label)
		}
	}
}

// The studio generates one command per instrument, and cobra captures its
// subcommands when the tree is built. A rig read after that point meant
// `cli studio guitar2` was not a command on a rig with a second guitar.
func TestInstrumentCommandsFollowTheRig(t *testing.T) {
	restore := studio.Current()
	defer func() { _ = studio.Use(restore) }()

	if err := studio.Use(studio.Topology{
		Main: restore.Main, Cues: restore.Cues,
		Instruments: []studio.Instrument{
			{ID: "guitar", Name: "Guitar", Input: 1, Kind: studio.KindGuitar},
			{ID: "guitar2", Name: "Guitar 2", Input: 2, Kind: studio.KindGuitar},
			{ID: "sax", Name: "Sax", Input: 3, Kind: studio.KindLine},
		},
	}); err != nil {
		t.Fatalf("the rig was refused: %v", err)
	}

	have := map[string]bool{}
	for _, c := range newStudioCmd(&env{}).Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"guitar", "guitar2", "sax"} {
		if !have[want] {
			t.Errorf("cli studio %s is not a command", want)
		}
	}
	// And an instrument that left the rig takes its command with it.
	if have["bass"] {
		t.Error("cli studio bass exists on a rig with no bass")
	}
}

// --config has to be honoured by the probes that run before the flag parser
// does, or a named file shapes half the run and the default file the rest.
func TestConfigFlagIsFoundBeforeParsing(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"studio", "--config", "/tmp/a.yaml"}, "/tmp/a.yaml"},
		{[]string{"--config=/tmp/b.yaml", "studio"}, "/tmp/b.yaml"},
		{[]string{"studio", "setup"}, ""},
		{[]string{"studio", "--config"}, ""},
	} {
		if got := configFlagValue(tc.args); got != tc.want {
			t.Errorf("configFlagValue(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
