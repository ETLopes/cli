package score

import (
	"math"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/lyrics"
)

func TestTendencyIsSharpFlatOrCenteredAroundAFifteenCentDeadZone(t *testing.T) {
	m := newMelody(4)
	cases := []struct {
		name   string
		offset float64
		want   Tendency
	}{
		{"thirty cents up is sharp", 0.30, TendencySharp},
		{"thirty cents down is flat", -0.30, TendencyFlat},
		{"five cents up is centered", 0.05, TendencyCentered},
		{"five cents down is centered", -0.05, TendencyCentered},
		{"exactly on pitch is centered", 0, TendencyCentered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(m, Config{Difficulty: Hard}, singer{offset: c.offset}.frames(m.ref))
			if got.Tendency != c.want {
				t.Errorf("tendency = %v, want %v (mean %.2f cents)", got.Tendency, c.want, got.MeanSignedCents)
			}
			if math.Abs(got.MeanSignedCents-c.offset*100) > 0.01 {
				t.Errorf("mean signed = %.3f cents, want %.3f", got.MeanSignedCents, c.offset*100)
			}
			if math.Abs(got.MeanAbsCents-math.Abs(c.offset)*100) > 0.01 {
				t.Errorf("mean abs = %.3f cents, want %.3f", got.MeanAbsCents, math.Abs(c.offset)*100)
			}
		})
	}
}

func TestTheDeadZoneEdgeIsCentered(t *testing.T) {
	if classify(15, 1) != TendencyCentered || classify(-15, 1) != TendencyCentered {
		t.Error("exactly 15 cents is still centered")
	}
	if classify(15.01, 1) != TendencySharp || classify(-15.01, 1) != TendencyFlat {
		t.Error("beyond 15 cents is sharp or flat")
	}
	if classify(0, 0) != TendencyUnknown {
		t.Error("no matched frames is unknown")
	}
}

func TestTheTendencyCountsMissedFramesToo(t *testing.T) {
	// 2.5 semitones sharp on Hard is a miss everywhere, but the singer is
	// plainly sharp, and the tendency says so.
	m := newMelody(3)
	got := run(m, Config{Difficulty: Hard}, singer{offset: 2.5}.frames(m.ref))
	if got.HitFrames != 0 || got.Tendency != TendencySharp || got.MeanAbsCents != 0 {
		t.Errorf("result = %+v", got)
	}
	if got.InTunePercent != 0 {
		t.Errorf("in tune = %v", got.InTunePercent)
	}
}

func TestInTunePercentIsHitVoicedFramesOverAllVoicedFrames(t *testing.T) {
	m := newMelody(8)
	got := run(m, medium, singer{to: 12 * time.Second}.frames(m.ref))
	if math.Abs(got.InTunePercent-50) > 0.01 {
		t.Errorf("in tune = %.3f%%, want 50", got.InTunePercent)
	}
}

func TestBestAndWorstLinesFollowTheHitRatio(t *testing.T) {
	m := newMelody(4)
	// Each line is 2 s of singing; the singer holds on for a fraction of it.
	fraction := []float64{0.6, 1.0, 0.2, 0.8}
	sung := singer{sing: func(t time.Duration) bool {
		line := int(t / (3 * time.Second))
		return float64(t-time.Duration(line)*3*time.Second) < fraction[line]*2*float64(time.Second)
	}}
	got := run(m, medium, sung.frames(m.ref))
	if got.BestLine != 1 || got.WorstLine != 2 {
		t.Errorf("best %d worst %d, want 1 and 2 (%+v)", got.BestLine, got.WorstLine, got.Lines)
	}
	if len(got.Lines) != 4 {
		t.Fatalf("lines = %d", len(got.Lines))
	}
	for i, l := range got.Lines {
		// The slack lets a cut-off note carry ~100 ms further.
		if want := fraction[i]; l.Ratio < want || l.Ratio > want+0.07 {
			t.Errorf("line %d ratio %.3f, want %.2f to %.2f", i, l.Ratio, want, want+0.07)
		}
	}
	sum := 0.0
	for _, l := range got.Lines {
		sum += l.Bonus
	}
	if math.Abs(sum-got.LineBonus) > 1e-6 {
		t.Errorf("line bonuses sum to %v, want %v", sum, got.LineBonus)
	}
}

func TestLinesWithoutVoicedFramesAreSkippedForBestAndWorst(t *testing.T) {
	ref := gridOf(9*time.Second, span{0, sec(1), 60}, span{sec(6), sec(7), 60})
	m := melody{ref: ref, lines: []lyrics.Line{
		{Start: 0, End: sec(3), Text: "alpha"},
		{Start: sec(3), End: sec(6), Text: "bravo"}, // wholly unsung in the reference
		{Start: sec(6), End: sec(9), Text: "charlie"},
	}}
	// Line 1 has no voiced frames of its own: the frames at 6.0 s belong to line 2.
	got := run(m, medium, singer{to: sec(1)}.frames(ref))
	if got.BestLine != 0 || got.WorstLine != 2 || len(got.Lines) != 2 {
		t.Errorf("best %d worst %d lines %+v", got.BestLine, got.WorstLine, got.Lines)
	}
}

func TestLongestStreakIsTheLongestRunOfConsecutiveHits(t *testing.T) {
	ref := gridOf(5*time.Second, span{0, 5 * time.Second, 60})
	m := melody{ref: ref}
	// Right for 1.5 s, wrong for 1 s, right for 2.5 s. The slack bridges 100 ms
	// of the wrong stretch at each edge, so the second run is 2.6 s.
	sung := singer{sing: func(time.Duration) bool { return true }}
	frames := sung.frames(ref)
	for i := range frames {
		if frames[i].T >= sec(1.5) && frames[i].T < sec(2.5) {
			frames[i].MIDI += 5
		}
	}
	got := run(m, medium, frames)
	if d := got.LongestStreak - sec(2.6); d < -20*time.Millisecond || d > 20*time.Millisecond {
		t.Errorf("longest streak = %v, want about 2.6s", got.LongestStreak)
	}
}

func TestUnvoicedReferenceFramesNeitherBreakNorExtendAStreak(t *testing.T) {
	ref := gridOf(4*time.Second, span{0, sec(1), 60}, span{sec(2), sec(3), 64})
	got := run(melody{ref: ref}, medium, singer{}.frames(ref))
	if got.LongestStreak != 2*time.Second {
		t.Errorf("longest streak = %v, want exactly 2s across the rest", got.LongestStreak)
	}
}

func TestAMissedFrameInTheMiddleSplitsTheStreak(t *testing.T) {
	ref := gridOf(3*time.Second, span{0, sec(1), 60}, span{sec(1), sec(2), 66}, span{sec(2), sec(3), 60})
	// The middle second is sung at 66+4 (a miss, and the neighbours' 60 is 6
	// away from 66, so the slack cannot rescue its middle).
	frames := singer{}.frames(ref)
	for i := range frames {
		if frames[i].T >= sec(1) && frames[i].T < sec(2) {
			frames[i].MIDI += 4
		}
	}
	got := run(melody{ref: ref}, Config{Difficulty: Hard}, frames)
	if got.LongestStreak >= 2*time.Second || got.LongestStreak < 900*time.Millisecond {
		t.Errorf("longest streak = %v, want about a second", got.LongestStreak)
	}
}
