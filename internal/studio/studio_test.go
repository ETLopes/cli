package studio

import (
	"strings"
	"testing"
)

// The topology is compiled in, so this guards the mistakes that would be worst
// to discover through the speakers: a shared input or a shared output.
func TestTopologyIsConsistent(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("topology is invalid: %v", err)
	}
}

func TestTopologyMatchesTheRig(t *testing.T) {
	wantInputs := map[string]int{
		"mic1": 1, "mic2": 2, "guitar": 3, "bass": 4, "keyboard": 5, "dtx": 7,
	}
	for id, want := range wantInputs {
		in, ok := LookupInstrument(id)
		if !ok {
			t.Fatalf("instrument %q is missing", id)
		}
		if in.Input != want {
			t.Errorf("%s is on input %d, want %d", id, in.Input, want)
		}
	}

	// Keyboard and DTX are wired mono. Declaring them stereo would pull in
	// whatever happens to be on the next input.
	for _, id := range []string{"keyboard", "dtx"} {
		in, _ := LookupInstrument(id)
		if in.Mode != Mono {
			t.Errorf("%s is %s, want mono in v1", id, in.Mode)
		}
		if got := in.Channels(); len(got) != 1 {
			t.Errorf("%s occupies %v, want a single channel", id, got)
		}
	}
}

func TestBusOutputsMatchTheInterface(t *testing.T) {
	if got := MainBus.Output.String(); got != "1/2" {
		t.Errorf("MAIN feeds %s, want 1/2 (the monitors)", got)
	}
	want := map[int]string{1: "3/4", 2: "5/6", 3: "7/8", 4: "9/10"}
	for cueID, outputs := range want {
		bus, ok := LookupCue(cueID)
		if !ok {
			t.Fatalf("cue %d is missing", cueID)
		}
		if got := bus.Output.String(); got != outputs {
			t.Errorf("CUE %d feeds %s, want %s", cueID, got, outputs)
		}
	}
}

func TestLookupInstrumentAcceptsHumanSpellings(t *testing.T) {
	for _, spelling := range []string{"mic1", "Mic 1", "MIC-1", "mic_1", "Mic1"} {
		in, ok := LookupInstrument(spelling)
		if !ok || in.ID != "mic1" {
			t.Errorf("LookupInstrument(%q) = %+v, %v; want mic1", spelling, in, ok)
		}
	}
	if _, ok := LookupInstrument("trombone"); ok {
		t.Error("an unknown instrument should not resolve")
	}
}

func TestParseAdjustment(t *testing.T) {
	tests := []struct {
		in       string
		delta    Level
		relative bool
		wantErr  bool
	}{
		{in: "+3", delta: 3, relative: true},
		{in: "-2", delta: -2, relative: true},
		{in: "0", delta: 0, relative: false},
		{in: "-6.5", delta: -6.5, relative: true},
		{in: "3", delta: 3, relative: false},
		{in: " +3 ", delta: 3, relative: true},
		{in: "off", delta: MinLevel, relative: false},
		{in: "mute", delta: MinLevel, relative: false},
		{in: "loud", wantErr: true},
		{in: "", wantErr: true},
		{in: "NaN", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseAdjustment(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseAdjustment(%q) should have failed", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAdjustment(%q): %v", tt.in, err)
			continue
		}
		if got.Delta != tt.delta || got.Relative != tt.relative {
			t.Errorf("ParseAdjustment(%q) = %+v, want delta=%v relative=%v",
				tt.in, got, tt.delta, tt.relative)
		}
	}
}

// A signed value means "change by this much", which is how someone at a desk
// asks for more guitar without first looking up the current level.
func TestRelativeAdjustmentAppliesToCurrentLevel(t *testing.T) {
	adj, _ := ParseAdjustment("+3")
	got, err := adj.Apply(2, MaxSendLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5 {
		t.Errorf("2 dB with %q = %v, want 5", "+3", got)
	}

	abs, _ := ParseAdjustment("3")
	got, err = abs.Apply(2, MaxSendLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3 {
		t.Errorf("2 dB with absolute 3 = %v, want 3", got)
	}
}

// A mistyped "+40" must be refused, not applied to someone's headphones.
func TestOversizedRelativeChangeIsRefused(t *testing.T) {
	for _, raw := range []string{"+40", "-40"} {
		adj, err := ParseAdjustment(raw)
		if err != nil {
			t.Fatalf("ParseAdjustment(%q): %v", raw, err)
		}
		if _, err := adj.Apply(0, MaxSendLevel); err == nil {
			t.Errorf("a change of %s should be refused as implausible", raw)
		}
	}
}

func TestRelativeChangesClampRatherThanOvershoot(t *testing.T) {
	adj, _ := ParseAdjustment("+6")
	got, err := adj.Apply(MaxSendLevel-1, MaxSendLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != MaxSendLevel {
		t.Errorf("got %v, want the level clamped to %v", got, MaxSendLevel)
	}

	adj, _ = ParseAdjustment("-12")
	got, err = adj.Apply(MinLevel+2, MaxSendLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != MinLevel {
		t.Errorf("got %v, want the level clamped to %v", got, MinLevel)
	}
}

// Boosting the control room above unity is never what someone means, and
// getting it wrong is painful.
func TestMonitorCannotBeBoostedAboveUnity(t *testing.T) {
	s := NewSession("test")
	adj, _ := ParseAdjustment("+6")
	got, err := s.SetMonitorVolume(adj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.To > MaxMonitorLevel {
		t.Errorf("monitor reached %v, want no more than %v", got.To, MaxMonitorLevel)
	}

	abs, _ := ParseAdjustment("6")
	if _, err := s.SetMonitorVolume(abs); err == nil {
		t.Error("an absolute monitor level above unity should be refused")
	}
}

// A fresh session must not make a loud noise the moment routing appears.
func TestNewSessionStartsSafe(t *testing.T) {
	s := NewSession("rehearsal")
	m := s.Monitor()
	if !m.Muted {
		t.Error("a new session should start muted")
	}
	if m.Volume > SafeStartupLevel {
		t.Errorf("monitor starts at %v, want no louder than %v", m.Volume, SafeStartupLevel)
	}
	if s.Name != "rehearsal" {
		t.Errorf("Name = %q, want rehearsal", s.Name)
	}
	if got := len(s.Cues()); got != CueCount() {
		t.Errorf("got %d cue mixes, want %d", got, CueCount())
	}
	for _, cue := range s.Cues() {
		for _, in := range Instruments() {
			if lvl := cue.Level(in.ID); lvl != Unity {
				t.Errorf("cue %d %s starts at %v, want unity", cue.ID, in.ID, lvl)
			}
		}
	}
}

func TestSetCueLevel(t *testing.T) {
	s := NewSession("test")
	adj, _ := ParseAdjustment("+3")
	change, err := s.SetCueLevel(1, "guitar", adj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if change.From != 0 || change.To != 3 {
		t.Errorf("change = %s, want 0 -> +3", change)
	}

	cue, _ := s.Cue(1)
	if got := cue.Level("guitar"); got != 3 {
		t.Errorf("cue 1 guitar = %v, want 3", got)
	}
	// One musician's mix must never move another's.
	other, _ := s.Cue(2)
	if got := other.Level("guitar"); got != Unity {
		t.Errorf("cue 2 guitar = %v, want it untouched at unity", got)
	}
}

func TestSetCueLevelRejectsUnknownTargets(t *testing.T) {
	s := NewSession("test")
	adj, _ := ParseAdjustment("+3")

	if _, err := s.SetCueLevel(9, "guitar", adj); err == nil {
		t.Error("cue 9 does not exist and should be refused")
	}
	_, err := s.SetCueLevel(1, "trombone", adj)
	if err == nil {
		t.Fatal("an unknown instrument should be refused")
	}
	// The error should say what is available rather than just "unknown".
	if !strings.Contains(err.Error(), "guitar") {
		t.Errorf("error %q should list the available instruments", err)
	}
}

func TestEffectToggling(t *testing.T) {
	s := NewSession("test")
	if s.EffectEnabled("guitar", "overdrive") {
		t.Error("overdrive should start off")
	}
	if err := s.SetEffect("guitar", "overdrive", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.EffectEnabled("guitar", "overdrive") {
		t.Error("overdrive should be on")
	}

	enabled := s.EnabledEffects("guitar")
	if len(enabled) != 1 || enabled[0].ID != "overdrive" {
		t.Errorf("EnabledEffects = %+v, want just overdrive", enabled)
	}
}

func TestEffectErrorsAreInformative(t *testing.T) {
	s := NewSession("test")
	err := s.SetEffect("guitar", "bagpipes", true)
	if err == nil {
		t.Fatal("expected an error for an effect not in the chain")
	}
	// The message should name what is available rather than just refusing.
	if !strings.Contains(err.Error(), "overdrive") {
		t.Errorf("error %q should list the available effects", err)
	}

	if err := s.SetEffect("trombone", "overdrive", true); err == nil {
		t.Error("expected an error for an unknown instrument")
	}
}

// Order is asserted as relationships rather than an exact list, so adding a
// pedal does not fail a test that has nothing to say about it. These are the
// orderings that actually change how the chain sounds.
func TestChainsFollowPedalboardOrder(t *testing.T) {
	pos := func(chain []Effect, id string) int {
		for i, e := range chain {
			if e.ID == id {
				return i
			}
		}
		return -1
	}

	for _, tc := range []struct {
		instrument string
		before     string
		after      string
		why        string
	}{
		{"guitar", "tuner", "overdrive", "a tuner needs the clean signal"},
		{"guitar", "compressor", "amp", "dynamics belong in front of the amp"},
		{"guitar", "overdrive", "gate", "the gate exists to catch what the drive adds"},
		{"guitar", "amp", "delay", "time effects sit after the amp"},
		{"guitar", "amp", "reverb", "time effects sit after the amp"},
		{"guitar", "chorus", "delay", "modulation before delay"},
		{"bass", "tuner", "compressor", "a tuner needs the clean signal"},
		{"bass", "compressor", "amp", "dynamics belong in front of the amp"},
		{"mic1", "compressor", "autotune", "correction tracks a steady level better"},
		{"mic1", "deesser", "reverb", "tame sibilance before adding space"},
	} {
		chain := Chain(tc.instrument)
		b, a := pos(chain, tc.before), pos(chain, tc.after)
		if b < 0 || a < 0 {
			t.Errorf("%s: missing %q or %q", tc.instrument, tc.before, tc.after)
			continue
		}
		if b > a {
			t.Errorf("%s: %q should come before %q (%s)",
				tc.instrument, tc.before, tc.after, tc.why)
		}
	}
}

// A chain that switched anything on by default would be a wall of sound the
// moment an instrument was plugged in.
func TestEverythingStartsOff(t *testing.T) {
	s := NewSession("test")
	for _, in := range Instruments() {
		for _, e := range Chain(in.ID) {
			if s.EffectEnabled(in.ID, e.ID) {
				t.Errorf("%s %s starts enabled; it should not", in.ID, e.ID)
			}
		}
	}
}

// Both microphones must offer the same chain, so either can take a lead vocal.
func TestBothMicrophonesShareAChain(t *testing.T) {
	a, b := Chain("mic1"), Chain("mic2")
	if len(a) != len(b) {
		t.Fatalf("mic1 has %d effects, mic2 has %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("position %d: mic1 has %q, mic2 has %q", i, a[i].ID, b[i].ID)
		}
	}
}

// Several chains hold more than one instance of the same plugin -- a vocal has
// three ReaTune instances -- so effect IDs must be unique within a chain or
// they cannot be told apart in REAPER.
func TestEffectIDsAreUniqueWithinAChain(t *testing.T) {
	for _, in := range Instruments() {
		seen := map[string]bool{}
		for _, e := range Chain(in.ID) {
			if seen[e.ID] {
				t.Errorf("%s has two effects with ID %q", in.ID, e.ID)
			}
			seen[e.ID] = true
			if e.Plugin == "" {
				t.Errorf("%s %s has no plugin", in.ID, e.ID)
			}
		}
	}
}

// An effect this program cannot fully configure must say so, or the player is
// left wondering why switching it on changed nothing.
func TestUnconfigurableEffectsExplainThemselves(t *testing.T) {
	for _, in := range Instruments() {
		for _, e := range Chain(in.ID) {
			if e.ID == "hardtune" || e.ID == "autotune" {
				if e.NeedsSetup == "" {
					t.Errorf("%s %s needs setup notes", in.ID, e.ID)
				}
			}
			if e.ID == "amp" && !e.NeedsIR {
				t.Errorf("%s amp is a convolution modeler and should be marked NeedsIR", in.ID)
			}
		}
	}
}

func TestSessionRoundTrips(t *testing.T) {
	s := NewSession("rehearsal")
	adj, _ := ParseAdjustment("+3")
	if _, err := s.SetCueLevel(1, "guitar", adj); err != nil {
		t.Fatal(err)
	}
	adj, _ = ParseAdjustment("-2")
	if _, err := s.SetCueLevel(1, "bass", adj); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEffect("guitar", "overdrive", true); err != nil {
		t.Fatal(err)
	}
	s.SetMonitorMute(false)
	adj, _ = ParseAdjustment("-3")
	if _, err := s.SetMonitorVolume(adj); err != nil {
		t.Fatal(err)
	}

	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}

	if got.Name != "rehearsal" {
		t.Errorf("Name = %q, want rehearsal", got.Name)
	}
	cue, _ := got.Cue(1)
	if cue.Level("guitar") != 3 {
		t.Errorf("cue 1 guitar = %v, want 3", cue.Level("guitar"))
	}
	if cue.Level("bass") != -2 {
		t.Errorf("cue 1 bass = %v, want -2", cue.Level("bass"))
	}
	if !got.EffectEnabled("guitar", "overdrive") {
		t.Error("overdrive should have survived the round trip")
	}
	if m := got.Monitor(); m.Muted || m.Volume != SafeStartupLevel-3 {
		t.Errorf("monitor = %+v, want unmuted at %v", m, SafeStartupLevel-3)
	}
}

// The file is meant to be hand-edited, so a bad value must be caught rather
// than turned into a routing change.
func TestUnmarshalRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"send far too loud",
			"session:\n  name: x\ncues:\n  1:\n    guitar: 40\n",
			"out of range",
		},
		{
			"monitor above unity",
			"session:\n  name: x\nmonitor:\n  volume: 12\n",
			"out of range",
		},
		{
			"unknown instrument",
			"session:\n  name: x\ncues:\n  1:\n    trombone: 0\n",
			"unknown instrument",
		},
		{
			"unknown cue",
			"session:\n  name: x\ncues:\n  9:\n    guitar: 0\n",
			"cue 9",
		},
		{
			"unknown effect",
			"session:\n  name: x\nfx:\n  guitar:\n    bagpipes: true\n",
			"no effect",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unmarshal([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected %s to be rejected", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A partial file is a normal way to work; anything unmentioned keeps defaults.
func TestUnmarshalFillsDefaults(t *testing.T) {
	s, err := Unmarshal([]byte("session:\n  name: quick\ncues:\n  1:\n    guitar: 3\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(s.Cues()); got != CueCount() {
		t.Errorf("got %d cues, want all %d present", got, CueCount())
	}
	cue, _ := s.Cue(2)
	if cue.Level("guitar") != Unity {
		t.Errorf("unmentioned cue 2 guitar = %v, want unity", cue.Level("guitar"))
	}
}

func TestLevelFormatting(t *testing.T) {
	for _, tt := range []struct {
		in   Level
		want string
	}{
		{3, "+3"}, {-2, "-2"}, {0, "0"}, {-6.5, "-6.5"}, {MinLevel, "-inf"},
	} {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Level(%v).String() = %q, want %q", float64(tt.in), got, tt.want)
		}
	}
}

// "-20" means twenty decibels quieter, so absolute negative levels need a
// spelling of their own. Without one, "set the monitors to -20 dB" cannot be
// expressed at all: the relative reading is refused as an implausible jump.
func TestAbsoluteNegativeLevels(t *testing.T) {
	adj, err := ParseAdjustment("@-20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adj.Relative {
		t.Error("@-20 should be absolute")
	}
	if adj.Delta != -20 {
		t.Errorf("delta = %v, want -20", adj.Delta)
	}

	// Applying it sets the level outright, regardless of where it was.
	got, err := adj.Apply(-3, MaxMonitorLevel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != -20 {
		t.Errorf("got %v, want -20", got)
	}

	// The relative spelling of the same number is still refused as too big.
	rel, _ := ParseAdjustment("-20")
	if _, err := rel.Apply(-3, MaxMonitorLevel); err == nil {
		t.Error("-20 as a relative change should still be refused")
	}

	if _, err := ParseAdjustment("@"); err == nil {
		t.Error("a bare '@' should be rejected")
	}
	if _, err := ParseAdjustment("@abc"); err == nil {
		t.Error("'@abc' should be rejected")
	}
}

func TestAbsolutePositiveStillWorks(t *testing.T) {
	adj, err := ParseAdjustment("@3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adj.Relative || adj.Delta != 3 {
		t.Errorf("@3 = %+v, want absolute 3", adj)
	}
}
