package reference

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// contour builds a 16 ms contour whose frames are described by parallel
// slices, defaulting to loud so a test only states what it is about.
func contour(hz, conf []float64) *Contour {
	n := len(hz)
	c := &Contour{HopSeconds: 0.016, SampleRate: 16000}
	for i := 0; i < n; i++ {
		c.Time = append(c.Time, float64(i)*0.016)
		c.Hz = append(c.Hz, hz[i])
		c.Confidence = append(c.Confidence, conf[i])
		c.LoudnessDB = append(c.LoudnessDB, -20)
	}
	return c
}

// permissive lets every frame with a pitch through, so a test can isolate the
// behaviour it is about. The loudness floor cannot simply be zero: 0 dBFS is a
// real threshold that nothing but a full-scale signal clears.
var permissive = Gates{MinLoudnessDB: -120}

func same(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestMIDIConvertsHzToFractionalSemitones(t *testing.T) {
	c := contour([]float64{440, 261.6256, 0}, same(0.9, 3))
	if got := c.MIDI(0); math.Abs(got-69) > 1e-9 {
		t.Errorf("440 Hz = %v, want 69", got)
	}
	if got := c.MIDI(1); math.Abs(got-60) > 1e-3 {
		t.Errorf("261.63 Hz = %v, want about 60", got)
	}
	if got := c.MIDI(2); !math.IsNaN(got) {
		t.Errorf("0 Hz = %v, want NaN", got)
	}
}

func TestHzToMIDIKeepsFractionsBetweenNotes(t *testing.T) {
	// A quarter tone above A4.
	got := HzToMIDI(440 * math.Pow(2, 0.5/12))
	if math.Abs(got-69.5) > 1e-9 {
		t.Errorf("got %v, want 69.5", got)
	}
}

func TestGatingDropsLowConfidenceFrames(t *testing.T) {
	c := contour(same(220, 8), []float64{0.9, 0.9, 0.9, 0.9, 0.4, 0.9, 0.9, 0.9}).WithGates(Gates{MinConfidence: 0.5, MinLoudnessDB: -120})
	if c.Voiced(4) {
		t.Error("a 0.4 confidence frame must be unvoiced")
	}
	if !c.Voiced(0) || !c.Voiced(7) {
		t.Error("confident frames must stay voiced")
	}
}

func TestGatingDropsQuietFramesAsSeparationResidue(t *testing.T) {
	c := contour(same(220, 6), same(0.9, 6))
	c.LoudnessDB[2] = -60
	c = c.WithGates(Gates{MinLoudnessDB: -45})
	if c.Voiced(2) {
		t.Error("a -60 dBFS frame is Demucs residue, not singing")
	}
	if !c.Voiced(1) {
		t.Error("a -20 dBFS frame should stay voiced")
	}
}

func TestGatingDropsFramesWithNoPitch(t *testing.T) {
	c := contour([]float64{220, 0, 220}, same(0.9, 3)).WithGates(permissive)
	if c.Voiced(1) {
		t.Error("0 Hz cannot be voiced however confident the detector is")
	}
}

func TestGatingDropsRunsShorterThanTheMinimum(t *testing.T) {
	// A 3-frame run (48 ms) is a blip; the 6-frame run (96 ms) is a note.
	conf := []float64{0.9, 0.9, 0.9, 0.1, 0.9, 0.9, 0.9, 0.9, 0.9, 0.9}
	c := contour(same(220, 10), conf).WithGates(Gates{MinConfidence: 0.5, MinLoudnessDB: -120, MinRun: 50 * time.Millisecond})
	for i := 0; i < 3; i++ {
		if c.Voiced(i) {
			t.Errorf("frame %d belongs to a 48 ms run and should be dropped", i)
		}
	}
	for i := 4; i < 10; i++ {
		if !c.Voiced(i) {
			t.Errorf("frame %d belongs to a 96 ms run and should stay", i)
		}
	}
}

func TestVoicedIsFalseOutOfRange(t *testing.T) {
	c := contour(same(220, 2), same(0.9, 2)).WithGates(DefaultGates())
	if c.Voiced(-1) || c.Voiced(2) {
		t.Error("out-of-range frames are unvoiced")
	}
}

func TestDefaultGatesMatchTheDocumentedThresholds(t *testing.T) {
	g := DefaultGates()
	if g.MinConfidence != 0.5 || g.MinLoudnessDB != -45 || g.MinRun != 50*time.Millisecond {
		t.Errorf("got %+v", g)
	}
}

func TestGridInterpolatesInsideAVoicedSpan(t *testing.T) {
	// Two voiced frames one semitone apart, 16 ms apart.
	c := contour([]float64{440, 440 * math.Pow(2, 1.0/12)}, same(0.9, 2)).WithGates(permissive)
	g := c.Grid(10 * time.Millisecond)
	// t=0 -> 69, t=10ms is 10/16 of the way to 70.
	if m, ok := g.At(0); !ok || math.Abs(m-69) > 1e-9 {
		t.Errorf("t=0: %v %v", m, ok)
	}
	if m, ok := g.At(10 * time.Millisecond); !ok || math.Abs(m-(69+10.0/16)) > 1e-9 {
		t.Errorf("t=10ms: %v %v", m, ok)
	}
}

func TestGridDoesNotInterpolateAcrossUnvoicedGaps(t *testing.T) {
	// Voiced, then an 8-frame gap, then voiced: nothing may be invented in the
	// middle even though the endpoints have well-defined MIDI values.
	hz := []float64{220, 220, 0, 0, 0, 0, 0, 0, 0, 0, 440, 440}
	c := contour(hz, same(0.9, len(hz))).WithGates(permissive)
	g := c.Grid(10 * time.Millisecond)
	for ms := 50; ms <= 110; ms += 10 {
		if _, ok := g.At(time.Duration(ms) * time.Millisecond); ok {
			t.Errorf("t=%dms is inside the gap and must be unvoiced", ms)
		}
	}
	if _, ok := g.At(160 * time.Millisecond); !ok {
		t.Error("t=160ms is a voiced frame and must be voiced")
	}
}

func TestGridLengthCoversTheContourAtTheNewStep(t *testing.T) {
	c := contour(same(220, 11), same(0.9, 11)).WithGates(permissive) // 0..160 ms
	g := c.Grid(10 * time.Millisecond)
	if len(g.MIDI) != 17 || len(g.Voiced) != 17 {
		t.Errorf("got %d/%d points, want 17 (0..160 ms inclusive)", len(g.MIDI), len(g.Voiced))
	}
	if g.Step != 10*time.Millisecond {
		t.Errorf("step = %v", g.Step)
	}
}

func TestGridAtOutsideTheGridIsUnvoiced(t *testing.T) {
	g := contour(same(220, 3), same(0.9, 3)).WithGates(permissive).Grid(10 * time.Millisecond)
	if _, ok := g.At(-time.Second); ok {
		t.Error("negative time is unvoiced")
	}
	if _, ok := g.At(time.Hour); ok {
		t.Error("time past the end is unvoiced")
	}
}

func TestContourRoundTripsThroughJSONFile(t *testing.T) {
	c := contour([]float64{220, 221, 222, 223, 224, 0}, []float64{0.9, 0.8, 0.9, 0.9, 0.9, 0.1})
	path := filepath.Join(t.TempDir(), "reference.json")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 6 || got.Hop() != 16*time.Millisecond || got.SampleRate != 16000 {
		t.Errorf("got %+v", got)
	}
	if got.Hz[1] != 221 || got.Confidence[5] != 0.1 {
		t.Errorf("values changed in transit: %+v", got)
	}
	if !got.Voiced(0) {
		t.Error("Load should apply the default gates")
	}
}

func TestLoadRejectsMismatchedArrays(t *testing.T) {
	c := contour([]float64{220, 221}, same(0.9, 2))
	c.Confidence = c.Confidence[:1]
	path := filepath.Join(t.TempDir(), "reference.json")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected an error for ragged arrays")
	}
}

func TestLoadRejectsAMissingFileAndBadJSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
