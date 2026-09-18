package studio

import "testing"

// The point of the whole feature: three inputs set to guitar are three
// guitarists with three pedalboards, not one board shared three ways.
func TestThreeGuitarsGetThreeSeparateChains(t *testing.T) {
	restore := Current()
	defer func() { _ = Use(restore) }()

	rig := Topology{
		Main: restore.Main, Cues: restore.Cues,
		Instruments: []Instrument{
			{ID: "guitar", Name: "Guitar", Input: 1, Mode: Mono, Kind: KindGuitar},
			{ID: "guitar2", Name: "Guitar 2", Input: 2, Mode: Mono, Kind: KindGuitar},
			{ID: "guitar3", Name: "Guitar 3", Input: 3, Mode: Mono, Kind: KindGuitar},
		},
	}
	if err := Use(rig); err != nil {
		t.Fatalf("three guitars is a legal rig: %v", err)
	}

	for _, id := range []string{"guitar", "guitar2", "guitar3"} {
		chain := Chain(id)
		if len(chain) == 0 {
			t.Fatalf("%s has no pedalboard", id)
		}
		if _, ok := LookupEffect(id, "overdrive"); !ok {
			t.Errorf("%s cannot reach its overdrive", id)
		}
	}

	// Each is its own instrument, so the FX page offers all three.
	if got := InstrumentsWithChains(); len(got) != 3 {
		t.Errorf("FX page offers %d instruments, want 3: %v", len(got), got)
	}
}

// A rig written before kinds existed names its inputs mic1, guitar, dtx. Those
// names already say what they are, so the file keeps working untouched.
func TestKindIsInferredFromOlderConfigs(t *testing.T) {
	for id, want := range map[string]Kind{
		"mic1": KindVocal, "mic2": KindVocal, "vox": KindVocal,
		"guitar": KindGuitar, "bass": KindBass,
		"keyboard": KindKeys, "synth": KindKeys,
		"dtx": KindDrums, "drums": KindDrums,
		"sax": KindLine, "": KindLine,
	} {
		if got := InferKind(id); got != want {
			t.Errorf("InferKind(%q) = %q, want %q", id, got, want)
		}
	}
}

// An instrument with no kind still has to resolve to a chain, or an old
// session would silently lose every effect it had.
func TestUnsetKindStillResolves(t *testing.T) {
	in := Instrument{ID: "mic1", Name: "Mic 1", Input: 1}
	if got := in.EffectiveKind(); got != KindVocal {
		t.Fatalf("EffectiveKind() = %q, want vocal", got)
	}
}

// The first of a kind reads plainly and later ones are numbered: a rig that
// only ever has one guitar should not have to call it Guitar 1.
func TestNamingAnAddedInstrument(t *testing.T) {
	existing := []Instrument{
		{ID: "mic1", Kind: KindVocal}, {ID: "mic2", Kind: KindVocal},
		{ID: "guitar", Kind: KindGuitar},
	}
	for _, tc := range []struct {
		kind         Kind
		wantID, want string
	}{
		{KindGuitar, "guitar2", "Guitar 2"},
		{KindVocal, "mic3", "Mic 3"},
		{KindBass, "bass", "Bass"},
		{KindLine, "line", "Line"},
	} {
		id, name := NextInstrument(tc.kind, existing)
		if id != tc.wantID || name != tc.want {
			t.Errorf("NextInstrument(%q) = %q/%q, want %q/%q",
				tc.kind, id, name, tc.wantID, tc.want)
		}
	}
}

// Two inputs sharing an ID would send one musician's effects to another, so a
// generated name must never collide even when the count suggests it is free.
func TestGeneratedNamesNeverCollide(t *testing.T) {
	existing := []Instrument{
		{ID: "guitar2", Kind: KindGuitar},
	}
	id, name := NextInstrument(KindGuitar, existing)
	if id == "guitar2" {
		t.Fatal("generated an ID that is already taken")
	}
	def := DefaultTopology()
	rig := Topology{
		Main: def.Main, Cues: def.Cues,
		Instruments: []Instrument{
			{ID: "guitar2", Name: "Guitar 2", Input: 1, Kind: KindGuitar},
			{ID: id, Name: name, Input: 2, Kind: KindGuitar},
		},
	}
	if err := rig.Validate(); err != nil {
		t.Errorf("the generated instrument clashes with the one it was named around: %v", err)
	}
}

// Every kind has to carry a chain. One that does not would be an input the
// interface offers and then has nothing to show for.
func TestEveryKindHasAChain(t *testing.T) {
	for _, k := range Kinds() {
		if len(ChainFor(k)) == 0 {
			t.Errorf("kind %q has no effects", k)
		}
	}
}

// Cycling has to visit every kind and come back, since it is the only way the
// interface offers to change one.
func TestCyclingVisitsEveryKind(t *testing.T) {
	seen := map[Kind]bool{}
	k := Kinds()[0]
	for range Kinds() {
		seen[k] = true
		k = k.Next()
	}
	if k != Kinds()[0] {
		t.Errorf("a full cycle ended on %q, not where it started", k)
	}
	if len(seen) != len(Kinds()) {
		t.Errorf("cycling saw %d of %d kinds", len(seen), len(Kinds()))
	}
	if got := Kinds()[0].Next().Prev(); got != Kinds()[0] {
		t.Errorf("next then previous landed on %q", got)
	}
}

// A kind that is set but misspelled is a typo worth reporting, not a silent
// fallback to a chain the player did not ask for.
func TestUnknownKindIsRefused(t *testing.T) {
	if _, err := ParseKind("trombone"); err == nil {
		t.Fatal("accepted an unknown kind")
	}
	err := Topology{
		Main:        DefaultTopology().Main,
		Cues:        DefaultTopology().Cues,
		Instruments: []Instrument{{ID: "x", Name: "X", Input: 1, Kind: Kind("trombone")}},
	}.Validate()
	if err == nil {
		t.Fatal("a topology with an unknown kind validated")
	}
}
