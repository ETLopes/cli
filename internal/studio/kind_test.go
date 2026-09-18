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

// A headphone jack on the interface is wired to a pair of line outputs and
// carries whatever they already hold. On this rig that is two mono cues, one
// per ear, which is exactly the thing worth naming.
func TestHeadphoneJacksCarryTheCuesOnTheirOutputs(t *testing.T) {
	restore := Current()
	defer func() { _ = Use(restore) }()
	if err := Use(DefaultTopology()); err != nil {
		t.Fatalf("the default rig was refused: %v", err)
	}

	if got := PhoneCount(); got != 2 {
		t.Fatalf("the interface has %d jacks, want 2", got)
	}
	for jack, want := range map[int][]string{
		1: {"CUE 5", "CUE 6"}, // line outputs 7/8
		2: {"CUE 7", "CUE 8"}, // line outputs 9/10
	} {
		cues, ok := PhoneCues(jack)
		if !ok {
			t.Fatalf("jack %d does not exist", jack)
		}
		if len(cues) != len(want) {
			t.Fatalf("jack %d carries %d cue(s), want %d", jack, len(cues), len(want))
		}
		for i, name := range want {
			if cues[i].Name != name {
				t.Errorf("jack %d ear %d is %q, want %q", jack, i+1, cues[i].Name, name)
			}
		}
	}

	// Asking the other way round has to agree, since that is what labels a
	// cue page with the jack it is also heard in.
	for _, c := range CueBuses() {
		jack, ear, ok := PhoneJackOf(c)
		switch c.Name {
		case "CUE 5":
			if !ok || jack != 1 || ear != "L" {
				t.Errorf("CUE 5 reports jack %d %q (ok=%v), want 1 L", jack, ear, ok)
			}
		case "CUE 8":
			if !ok || jack != 2 || ear != "R" {
				t.Errorf("CUE 8 reports jack %d %q (ok=%v), want 2 R", jack, ear, ok)
			}
		case "CUE 1", "CUE 2", "CUE 3", "CUE 4":
			if ok {
				t.Errorf("%s should reach no headphone jack, got %d", c.Name, jack)
			}
		}
	}
}

// The jacks share their channels with the cues on purpose, so the overlap
// check that guards the outputs must not refuse a perfectly good rig.
func TestHeadphoneJacksMayShareCueOutputs(t *testing.T) {
	def := DefaultTopology()
	if err := def.Validate(); err != nil {
		t.Fatalf("the default rig, whose jacks tap its cues, was refused: %v", err)
	}
	bad := DefaultTopology()
	bad.Phones = []OutputPair{{Left: 7}}
	if err := bad.Validate(); err == nil {
		t.Error("a mono headphone jack was accepted; a jack is stereo by construction")
	}
}

// An interface with no jacks configured still has the ones it physically has.
func TestHeadphoneJacksSurviveACueOnlyConfig(t *testing.T) {
	restore := Current()
	defer func() { _ = Use(restore) }()
	rig := DefaultTopology()
	rig.Cues = rig.Cues[:2]
	if err := Use(rig); err != nil {
		t.Fatalf("a two-cue rig was refused: %v", err)
	}
	if PhoneCount() != 2 {
		t.Errorf("the jacks vanished with the cue layout: %d", PhoneCount())
	}
	// Outputs 7-10 hold no cue now, so the jacks carry nothing. Saying so
	// beats reporting a mix that is not there.
	if cues, ok := PhoneCues(1); !ok || len(cues) != 0 {
		t.Errorf("jack 1 reports %d cue(s) on outputs nothing feeds", len(cues))
	}
}
