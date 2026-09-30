package ultrastar

import (
	"math"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/reference"
)

const step = 10 * time.Millisecond

// piece is a stretch of a synthetic grid: silence when midi is NaN.
type piece struct {
	ms   int
	midi float64
}

func silence(ms int) piece { return piece{ms, math.NaN()} }

func gridOf(pieces ...piece) reference.Grid {
	g := reference.Grid{Step: step}
	for _, p := range pieces {
		for i := 0; i < p.ms/10; i++ {
			g.MIDI = append(g.MIDI, p.midi)
			g.Voiced = append(g.Voiced, !math.IsNaN(p.midi))
		}
	}
	return g
}

func TestSegmentSteppedContourGivesThreeNotesInBeats(t *testing.T) {
	g := gridOf(piece{300, 60}, piece{300, 64}, piece{300, 67})
	notes := Segment(g, DefaultSegmentConfig())
	chart := ToChart(notes, DefaultTiming())
	if len(chart) != 3 {
		t.Fatalf("got %d notes, want 3: %+v", len(chart), chart)
	}
	want := []struct{ beat, length, pitch int }{{0, 30, 0}, {30, 30, 4}, {60, 30, 7}}
	for i, w := range want {
		c := chart[i]
		if c.Beat != w.beat || c.Length != w.length || c.Pitch != w.pitch {
			t.Errorf("note %d = beat %d len %d pitch %d, want %+v", i, c.Beat, c.Length, c.Pitch, w)
		}
	}
}

func TestSegmentVibratoWithinFortyCentsStaysOneNote(t *testing.T) {
	var g reference.Grid
	g.Step = step
	for i := 0; i < 100; i++ {
		tt := float64(i) * 0.01
		g.MIDI = append(g.MIDI, 62+0.4*math.Sin(2*math.Pi*5*tt))
		g.Voiced = append(g.Voiced, true)
	}
	notes := Segment(g, DefaultSegmentConfig())
	if len(notes) != 1 || notes[0].Pitch != 2 {
		t.Fatalf("notes = %+v, want one note at pitch 2", notes)
	}
}

func TestSegmentSplitsOnOneSemitoneStepHeldTwoHundredMilliseconds(t *testing.T) {
	notes := Segment(gridOf(piece{300, 60}, piece{200, 61}), DefaultSegmentConfig())
	if len(notes) != 2 || notes[0].Pitch != 0 || notes[1].Pitch != 1 {
		t.Fatalf("notes = %+v", notes)
	}
	if notes[1].Start != 300*time.Millisecond {
		t.Errorf("second note starts at %v, want 300ms", notes[1].Start)
	}
}

func TestSegmentIgnoresAJumpThatDoesNotLastFiftyMilliseconds(t *testing.T) {
	notes := Segment(gridOf(piece{300, 60}, piece{40, 61}, piece{300, 60}), DefaultSegmentConfig())
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want the glitch folded into one note", notes)
	}
}

func TestSegmentDropsBlipsUnderEightyMilliseconds(t *testing.T) {
	notes := Segment(gridOf(piece{300, 60}, silence(100), piece{70, 64}, silence(100), piece{300, 67}), DefaultSegmentConfig())
	if len(notes) != 2 || notes[0].Pitch != 0 || notes[1].Pitch != 7 {
		t.Fatalf("notes = %+v", notes)
	}
	// Exactly 80 ms is kept.
	if got := Segment(gridOf(piece{80, 60}), DefaultSegmentConfig()); len(got) != 1 {
		t.Errorf("an 80 ms note should survive, got %+v", got)
	}
}

func TestSegmentMergesSamePitchAcrossTwentyMsButNotSixty(t *testing.T) {
	cfg := DefaultSegmentConfig()
	merged := Segment(gridOf(piece{200, 60}, silence(20), piece{200, 60}), cfg)
	if len(merged) != 1 || merged[0].End != 420*time.Millisecond {
		t.Errorf("20 ms gap: %+v, want one note ending at 420ms", merged)
	}
	apart := Segment(gridOf(piece{200, 60}, silence(60), piece{200, 60}), cfg)
	if len(apart) != 2 {
		t.Errorf("60 ms gap: %+v, want two notes", apart)
	}
}

func TestSegmentDoesNotMergeDifferentPitchesAcrossASmallGap(t *testing.T) {
	notes := Segment(gridOf(piece{200, 60}, silence(20), piece{200, 62}), DefaultSegmentConfig())
	if len(notes) != 2 {
		t.Errorf("notes = %+v", notes)
	}
}

func TestSegmentVoicingGapSplitsNotes(t *testing.T) {
	notes := Segment(gridOf(piece{200, 60}, silence(200), piece{200, 60}), DefaultSegmentConfig())
	if len(notes) != 2 || notes[1].Start != 400*time.Millisecond {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestSegmentPitchesBelowC4AreNegative(t *testing.T) {
	notes := Segment(gridOf(piece{200, 55}, piece{200, 48}), DefaultSegmentConfig())
	if len(notes) != 2 || notes[0].Pitch != -5 || notes[1].Pitch != -12 {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestSegmentMedianFilterRemovesASingleFrameSpike(t *testing.T) {
	g := gridOf(piece{200, 60})
	g.MIDI[10] = 72
	notes := Segment(g, DefaultSegmentConfig())
	if len(notes) != 1 || notes[0].Pitch != 0 {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestSegmentEmptyGridGivesNoNotes(t *testing.T) {
	if got := Segment(reference.Grid{}, DefaultSegmentConfig()); got != nil {
		t.Errorf("got %+v", got)
	}
}

func TestToChartKeepsNotesFromOverlappingAndAtLeastOneBeatLong(t *testing.T) {
	notes := []Note{
		{Start: 0, End: 104 * time.Millisecond},
		{Start: 104 * time.Millisecond, End: 106 * time.Millisecond},
		{Start: 106 * time.Millisecond, End: 200 * time.Millisecond},
	}
	chart := ToChart(notes, DefaultTiming())
	prevEnd := 0
	for i, c := range chart {
		if c.Length < 1 || c.Beat < prevEnd {
			t.Errorf("note %d = %+v overlaps or is empty (prev end %d)", i, c, prevEnd)
		}
		prevEnd = c.Beat + c.Length
	}
}

func TestToChartRoundsHalfBeatsAwayFromZero(t *testing.T) {
	chart := ToChart([]Note{{Start: 25 * time.Millisecond, End: 100 * time.Millisecond}}, Timing{BPM: 1500})
	// 25 ms is 2.5 beats; half rounds up to 3.
	if chart[0].Beat != 3 || chart[0].Length != 7 {
		t.Errorf("chart = %+v, want beat 3 length 7", chart[0])
	}
}

func TestTimingSecondsAndBeatRoundTripWithinHalfABeat(t *testing.T) {
	for _, tm := range []Timing{DefaultTiming(), {BPM: 300, GapMS: 1234}} {
		for _, sec := range []float64{1.3, 1.2345, 61.5, 200.001} {
			back := tm.Seconds(math.Round(tm.Beat(sec)))
			if half := (tm.Seconds(1) - tm.Seconds(0)) / 2; math.Abs(back-sec) > half+1e-9 {
				t.Errorf("%+v: %v -> %v, off by more than half a beat", tm, sec, back)
			}
		}
	}
}

func TestDefaultTimingIsTenMillisecondBeats(t *testing.T) {
	tm := DefaultTiming()
	if got := tm.Seconds(1) - tm.Seconds(0); math.Abs(got-0.01) > 1e-12 {
		t.Errorf("beat = %v s, want 0.01", got)
	}
	if tm.GapMS != 0 {
		t.Errorf("GAP = %v", tm.GapMS)
	}
}
