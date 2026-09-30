package score

import (
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
)

const step = 10 * time.Millisecond

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// span is a stretch of the reference that is sung at a pitch.
type span struct {
	from, to time.Duration
	midi     float64
}

// gridOf builds a reference grid of the given length with the given voiced
// spans (half open: [from, to)).
func gridOf(total time.Duration, spans ...span) reference.Grid {
	n := int(total/step) + 1
	g := reference.Grid{Step: step, MIDI: make([]float64, n), Voiced: make([]bool, n)}
	for _, sp := range spans {
		for i := int(sp.from / step); i < int(sp.to/step) && i < n; i++ {
			g.MIDI[i], g.Voiced[i] = sp.midi, true
		}
	}
	return g
}

// melody is a synthetic song: `count` lines of 3 s, each 2 s of four 500 ms
// notes then a 1 s rest, so lines never bleed into each other through the
// timing slack.
type melody struct {
	ref   reference.Grid
	lines []lyrics.Line
}

func newMelody(count int) melody {
	var spans []span
	var lines []lyrics.Line
	for l := 0; l < count; l++ {
		start := time.Duration(l) * 3 * time.Second
		lines = append(lines, lyrics.Line{Start: start, End: start + 3*time.Second, Text: "alpha bravo"})
		for j := 0; j < 4; j++ {
			from := start + time.Duration(j)*500*time.Millisecond
			// Consecutive notes are at least 3 semitones apart, so a note is
			// never mistaken for its neighbour.
			spans = append(spans, span{from, from + 500*time.Millisecond, 57 + float64((l*5+j*4)%9)})
		}
	}
	return melody{ref: gridOf(time.Duration(count)*3*time.Second, spans...), lines: lines}
}

// singer produces player frames from a reference.
type singer struct {
	offset     float64       // semitones added to the target
	lag        time.Duration // positive sings late
	from, to   time.Duration // singing only happens in [from, to); zero to means always
	sing       func(t time.Duration) bool
	alwaysNote float64 // if non-zero, sing this pitch whenever singing
}

func (s singer) frames(ref reference.Grid) []Frame {
	// Run a second past the song so a late singer finishes their last note.
	end := time.Duration(len(ref.MIDI))*ref.Step + time.Second
	var out []Frame
	for t := time.Duration(0); t < end; t += step {
		f := Frame{T: t}
		midi, v := ref.At(t - s.lag)
		if s.alwaysNote != 0 {
			midi, v = s.alwaysNote, true
		}
		inWindow := t >= s.from && (s.to == 0 || t < s.to)
		if v && inWindow && (s.sing == nil || s.sing(t)) {
			f.MIDI, f.Voiced = midi+s.offset, true
		}
		out = append(out, f)
	}
	return out
}

func run(m melody, cfg Config, frames []Frame) Result {
	s := New(m.ref, m.lines, cfg)
	s.Push(frames...)
	return s.Finish()
}

var medium = Config{Difficulty: Medium}

func TestParseDifficultyReadsConfigValues(t *testing.T) {
	for in, want := range map[string]Difficulty{"easy": Easy, "Medium": Medium, " HARD ": Hard} {
		got, err := ParseDifficulty(in)
		if err != nil || got != want {
			t.Errorf("ParseDifficulty(%q) = %v, %v; want %v", in, got, err, want)
		}
		if got.String() != map[Difficulty]string{Easy: "easy", Medium: "medium", Hard: "hard"}[want] {
			t.Errorf("String() of %v = %q", want, got.String())
		}
	}
	if _, err := ParseDifficulty("insane"); err == nil {
		t.Error("ParseDifficulty(insane) should fail")
	}
}

func TestDifficultyToleranceGrowsAsItGetsEasier(t *testing.T) {
	if Easy.Tolerance() != 2.5 || Medium.Tolerance() != 1.5 || Hard.Tolerance() != 0.5 {
		t.Errorf("tolerances = %v %v %v", Easy.Tolerance(), Medium.Tolerance(), Hard.Tolerance())
	}
}

func TestAPlayerIdenticalToTheReferenceScoresExactlyTenThousand(t *testing.T) {
	m := newMelody(6)
	got := run(m, medium, singer{}.frames(m.ref))
	if got.Score != 10000 {
		t.Fatalf("score = %d, want 10000 (%+v)", got.Score, got)
	}
	if got.NotePoints != 9000 || got.LineBonus != 1000 {
		t.Errorf("subtotals = %v + %v", got.NotePoints, got.LineBonus)
	}
	if got.Incomplete {
		t.Error("a normal finish must not be incomplete")
	}
}

func TestAPlayerAnOctaveAwayScoresFullMarks(t *testing.T) {
	m := newMelody(4)
	for _, off := range []float64{12, -12, 24} {
		if got := run(m, Config{Difficulty: Hard}, singer{offset: off}.frames(m.ref)); got.Score != 10000 {
			t.Errorf("offset %+v semitones scored %d, want 10000", off, got.Score)
		}
	}
}

func TestAConstantOffsetOfOnePointTwoSemitonesFailsOnlyOnHard(t *testing.T) {
	m := newMelody(4)
	frames := singer{offset: 1.2}.frames(m.ref)
	for _, d := range []Difficulty{Easy, Medium} {
		if got := run(m, Config{Difficulty: d}, frames); got.Score != 10000 {
			t.Errorf("%v scored %d, want 10000", d, got.Score)
		}
	}
	hard := run(m, Config{Difficulty: Hard}, frames)
	if hard.NotePoints != 0 || hard.Score != 0 {
		t.Errorf("hard: note points %v score %d, want 0", hard.NotePoints, hard.Score)
	}
}

func TestATolerantBoundaryIsAHit(t *testing.T) {
	m := newMelody(2)
	if got := run(m, Config{Difficulty: Hard}, singer{offset: 0.5}.frames(m.ref)); got.Score != 10000 {
		t.Errorf("exactly 0.5 semitones on Hard scored %d, want 10000", got.Score)
	}
}

func TestLaggingWithinTheSlackStillScoresFull(t *testing.T) {
	m := newMelody(4)
	for _, lag := range []time.Duration{80 * time.Millisecond, -80 * time.Millisecond} {
		if got := run(m, medium, singer{lag: lag}.frames(m.ref)); got.Score != 10000 {
			t.Errorf("lag %v scored %d, want 10000", lag, got.Score)
		}
	}
}

// fastMelody changes note every 50 ms through six pitch classes two semitones
// apart, so no pitch recurs within the slack window. It is the worst case for
// timing: the slack is generous by design, and only a melody that changes
// faster than the slack can punish a singer who is late by more than it.
func fastMelody() melody {
	var spans []span
	for i := 0; i < 100; i++ {
		from := time.Duration(i) * 50 * time.Millisecond
		spans = append(spans, span{from, from + 50*time.Millisecond, 60 + float64(i%6)*2})
	}
	return melody{ref: gridOf(5*time.Second, spans...)}
}

func TestLaggingOrLeadingBeyondTheSlackLosesMostPoints(t *testing.T) {
	m := fastMelody()
	for _, lag := range []time.Duration{150 * time.Millisecond, -150 * time.Millisecond} {
		got := run(m, medium, singer{lag: lag}.frames(m.ref))
		if got.Score > 2000 {
			t.Errorf("lag %v scored %d, want under 2000 of 10000", lag, got.Score)
		}
		full := run(m, medium, singer{lag: 80 * time.Millisecond}.frames(m.ref))
		if full.Score != 10000 {
			t.Errorf("80 ms lag on the fast melody scored %d, want 10000", full.Score)
		}
	}
}

func TestAWiderConfiguredSlackForgivesALargerLag(t *testing.T) {
	m := fastMelody()
	got := run(m, Config{Difficulty: Medium, Slack: 200 * time.Millisecond}, singer{lag: 150 * time.Millisecond}.frames(m.ref))
	if got.Score != 10000 {
		t.Errorf("200 ms slack with 150 ms lag scored %d, want 10000", got.Score)
	}
}

func TestLaggingBeyondTheSlackOnSustainedNotesOnlyLosesTheirStarts(t *testing.T) {
	m := newMelody(4) // 500 ms notes: 150 ms lag misses only the first ~50 ms of each
	got := run(m, medium, singer{lag: 150 * time.Millisecond}.frames(m.ref))
	if got.Score >= 10000 || got.Score < 8500 {
		t.Errorf("score = %d, want a modest loss below 10000 and above 8500", got.Score)
	}
}

func TestSilenceScoresZero(t *testing.T) {
	m := newMelody(4)
	got := run(m, medium, singer{sing: func(time.Duration) bool { return false }}.frames(m.ref))
	if got.Score != 0 || got.HitFrames != 0 || got.Tendency != TendencyUnknown {
		t.Errorf("silence: %+v", got)
	}
}

func TestUnvoicedReferenceFramesNeverAwardPoints(t *testing.T) {
	// Only the first two seconds are to be sung, at pitch 60.
	ref := gridOf(6*time.Second, span{0, 2 * time.Second, 60})
	m := melody{ref: ref}
	everywhere := run(m, medium, singer{alwaysNote: 60}.frames(ref))
	onlyWhere := run(m, medium, singer{alwaysNote: 60, to: 2 * time.Second}.frames(ref))
	if everywhere.Score != 10000 || onlyWhere.Score != 10000 {
		t.Errorf("scores = %d and %d, want both 10000, never more", everywhere.Score, onlyWhere.Score)
	}
	if everywhere.HitFrames != everywhere.VoicedFrames || everywhere.VoicedFrames != 200 {
		t.Errorf("hits %d of %d voiced, want 200 of 200", everywhere.HitFrames, everywhere.VoicedFrames)
	}
}

func TestAReferenceWithNoVoicedFramesScoresZeroWithoutDividing(t *testing.T) {
	ref := gridOf(3 * time.Second)
	got := run(melody{ref: ref}, medium, singer{alwaysNote: 60}.frames(ref))
	if got.Score != 0 || got.InTunePercent != 0 || got.BestLine != -1 || got.WorstLine != -1 || len(got.Lines) != 0 {
		t.Errorf("no voiced frames: %+v", got)
	}
	empty := New(reference.Grid{}, nil, medium)
	empty.Push(Frame{T: time.Second, MIDI: 60, Voiced: true})
	if r := empty.Finish(); r.Score != 0 {
		t.Errorf("empty grid scored %d", r.Score)
	}
}

func TestSingingOnlyTheFirstHalfOfTheLinesScoresHalf(t *testing.T) {
	m := newMelody(8)
	got := run(m, medium, singer{to: 12 * time.Second}.frames(m.ref))
	voiced := 0
	for _, v := range m.ref.Voiced {
		if v {
			voiced++
		}
	}
	half := float64(voiced / 2)
	wantNote := 9000 * half / float64(voiced)
	wantBonus := 1000 * half / float64(voiced)
	if math.Abs(got.NotePoints-wantNote) > 1 || math.Abs(got.LineBonus-wantBonus) > 1 {
		t.Errorf("note %.2f bonus %.2f, want %.2f and %.2f", got.NotePoints, got.LineBonus, wantNote, wantBonus)
	}
	if math.Abs(float64(got.Score)-5000) > 1 {
		t.Errorf("score = %d, want 5000 within 1", got.Score)
	}
}

func TestWithoutLyricsFourSecondWindowsStandInForLinesAndTheCapHolds(t *testing.T) {
	m := newMelody(6) // 18 s: windows 0..4
	got := run(melody{ref: m.ref}, medium, singer{}.frames(m.ref))
	if got.Score != 10000 {
		t.Fatalf("score = %d, want 10000", got.Score)
	}
	if len(got.Lines) != 5 {
		t.Fatalf("lines = %d, want 5 windows", len(got.Lines))
	}
	sum := 0
	for i, l := range got.Lines {
		if l.Index != i {
			t.Errorf("line %d has index %d", i, l.Index)
		}
		sum += l.Voiced
	}
	if sum != got.VoicedFrames {
		t.Errorf("window voiced frames %d != %d", sum, got.VoicedFrames)
	}
}

func TestBlankLinesAreIgnoredWhenAssigningLines(t *testing.T) {
	ref := gridOf(6*time.Second, span{0, 2 * time.Second, 60})
	lines := []lyrics.Line{
		{Start: 0, End: 3 * time.Second, Text: "alpha"},
		{Start: 3 * time.Second, End: 6 * time.Second, Text: "  "},
	}
	got := run(melody{ref: ref, lines: lines}, medium, singer{}.frames(ref))
	if len(got.Lines) != 1 || got.Lines[0].Index != 0 || got.Lines[0].Voiced != 200 {
		t.Errorf("lines = %+v", got.Lines)
	}
}

func TestVoicedFramesOutsideEveryLyricLineJoinTheNearestLine(t *testing.T) {
	// Lines cover 2 s..3 s and 6 s..7 s; the reference sings 1..1.5 s (nearest:
	// line 0), 4.2..4.5 s (line 0, 1.2 s from its end vs 1.5 s), and 5..5.2 s
	// (line 1), plus one frame in line 1 itself.
	ref := gridOf(8*time.Second,
		span{sec(1), sec(1.5), 60}, span{sec(4.2), sec(4.5), 60},
		span{sec(5), sec(5.2), 60}, span{sec(6.5), sec(6.51), 60})
	lines := []lyrics.Line{
		{Start: sec(2), End: sec(3), Text: "alpha"},
		{Start: sec(6), End: sec(7), Text: "bravo"},
	}
	got := run(melody{ref: ref, lines: lines}, medium, singer{}.frames(ref))
	if len(got.Lines) != 2 || got.Lines[0].Voiced != 80 || got.Lines[1].Voiced != 21 {
		t.Errorf("lines = %+v, want 80 and 21 voiced frames", got.Lines)
	}
}

func TestLineBonusIsAwardedOnlyOnceALineIsFullyFinalized(t *testing.T) {
	m := newMelody(2)
	s := New(m.ref, m.lines, medium)
	frames := singer{}.frames(m.ref)
	// Push up to 1.5 s: line 0 is unfinished so only note points exist.
	var upTo int
	for upTo < len(frames) && frames[upTo].T <= sec(1.5) {
		upTo++
	}
	s.Push(frames[:upTo]...)
	early := s.Snapshot().Score
	voiced := 0
	for _, v := range m.ref.Voiced {
		if v {
			voiced++
		}
	}
	finalized := 0
	for i := 0; i < len(m.ref.Voiced) && time.Duration(i)*step+DefaultSlack < sec(1.5); i++ {
		if m.ref.Voiced[i] {
			finalized++
		}
	}
	want := int(math.Round(9000 * float64(finalized) / float64(voiced)))
	if early != want {
		t.Errorf("score mid-line = %d, want the note points alone: %d", early, want)
	}
	s.Push(frames[upTo:]...)
	if got := s.Snapshot().Score; got != 10000 {
		t.Errorf("score after all frames = %d, want 10000", got)
	}
}

func TestPushingInRandomChunksWithJitterAndDuplicatesMatchesPushingAllAtOnce(t *testing.T) {
	m := newMelody(6)
	rng := rand.New(rand.NewSource(7))
	sung := singer{offset: 0.7, sing: func(t time.Duration) bool { return (t/(700*time.Millisecond))%3 != 1 }}
	frames := sung.frames(m.ref)
	want := run(m, medium, frames)

	s := New(m.ref, m.lines, medium)
	prev := 0
	for i := 0; i < len(frames); {
		n := 1 + rng.Intn(40)
		chunk := append([]Frame(nil), frames[i:min(i+n, len(frames))]...)
		i += len(chunk)
		// Duplicates and jitter in order within the chunk.
		for _, f := range chunk[:len(chunk)/3] {
			chunk = append(chunk, f)
		}
		rng.Shuffle(len(chunk), func(a, b int) { chunk[a], chunk[b] = chunk[b], chunk[a] })
		s.Push(chunk...)
		if sc := s.Snapshot().Score; sc < prev {
			t.Fatalf("live score fell from %d to %d", prev, sc)
		} else {
			prev = sc
		}
	}
	got := s.Finish()
	if got.Score != want.Score || got.HitFrames != want.HitFrames || got.LongestStreak != want.LongestStreak ||
		got.MeanAbsCents != want.MeanAbsCents || got.MeanSignedCents != want.MeanSignedCents {
		t.Errorf("incremental %+v != batch %+v", got, want)
	}
	if want.Score == 0 || want.Score == 10000 {
		t.Errorf("scenario is degenerate: %d", want.Score)
	}
}

func TestFrameTimesWithJitterAndGapsStillScore(t *testing.T) {
	m := newMelody(3)
	rng := rand.New(rand.NewSource(3))
	var frames []Frame
	for _, f := range (singer{}).frames(m.ref) {
		if rng.Intn(10) == 0 { // gap
			continue
		}
		f.T += time.Duration(rng.Intn(9)-4) * time.Millisecond // +/-4 ms
		if f.T >= 0 {
			frames = append(frames, f)
		}
	}
	if got := run(m, medium, frames); got.Score < 9900 {
		t.Errorf("jittery gapped singing scored %d, want at least 9900", got.Score)
	}
}

func TestFinishingEarlyScoresOnlyWhatWasSungAgainstTheWholeSong(t *testing.T) {
	m := newMelody(8)
	frames := singer{to: 12 * time.Second}.frames(m.ref)
	s := New(m.ref, m.lines, medium)
	for _, f := range frames {
		if f.T > 12*time.Second {
			break
		}
		s.Push(f)
	}
	got := s.FinishEarly()
	if !got.Incomplete {
		t.Error("an early finish must be flagged incomplete")
	}
	if math.Abs(float64(got.Score)-5000) > 1 {
		t.Errorf("score = %d, want 5000 within 1", got.Score)
	}
	if again := s.Finish(); again.Incomplete != true || again.Score != got.Score {
		t.Errorf("Finish after FinishEarly changed the result: %+v", again)
	}
	s.Push(Frame{T: 20 * time.Second, MIDI: 60, Voiced: true})
	if s.Snapshot().Score != got.Score {
		t.Error("pushing after Finish must be ignored")
	}
}

func TestSnapshotReportsTheTargetStreakAndLastEvent(t *testing.T) {
	ref := gridOf(4*time.Second, span{0, 4 * time.Second, 62})
	s := New(ref, nil, medium)
	if snap := s.Snapshot(); snap.Last != EventNone || snap.Score != 0 {
		t.Errorf("fresh snapshot = %+v", snap)
	}
	for t0 := time.Duration(0); t0 <= sec(1.5); t0 += step {
		s.Push(Frame{T: t0, MIDI: 62, Voiced: true})
	}
	snap := s.Snapshot()
	if snap.Last != EventHit || !snap.TargetVoiced || snap.TargetMIDI != 62 || snap.Time != sec(1.5) {
		t.Errorf("snapshot = %+v", snap)
	}
	// Finalized: t with t+100ms < 1.5s, i.e. t = 0..1.39 s = 140 frames.
	if snap.Streak != 1400*time.Millisecond {
		t.Errorf("streak = %v, want 1.4s", snap.Streak)
	}
	for t0 := sec(1.51); t0 <= sec(2.5); t0 += step {
		s.Push(Frame{T: t0, MIDI: 70, Voiced: true})
	}
	if snap := s.Snapshot(); snap.Last != EventMiss || snap.Streak != 0 {
		t.Errorf("after wrong singing: %+v", snap)
	}
}

func TestSnapshotFromAnotherGoroutineIsRaceFree(t *testing.T) {
	m := newMelody(4)
	s := New(m.ref, m.lines, medium)
	frames := singer{}.frames(m.ref)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		prev := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			sc := s.Snapshot().Score
			if sc < prev {
				t.Errorf("live score fell from %d to %d", prev, sc)
				return
			}
			prev = sc
		}
	}()
	for i := 0; i < len(frames); i += 5 {
		s.Push(frames[i:min(i+5, len(frames))]...)
	}
	res := s.Finish()
	close(stop)
	wg.Wait()
	if res.Score != 10000 {
		t.Errorf("score = %d", res.Score)
	}
}

func TestNonFiniteAndUnorderedInputNeverBreaksTheScorer(t *testing.T) {
	m := newMelody(2)
	s := New(m.ref, m.lines, medium)
	s.Push(Frame{T: sec(1), MIDI: math.NaN(), Voiced: true}, Frame{T: sec(0.5), MIDI: math.Inf(1), Voiced: true},
		Frame{T: -sec(1), MIDI: 60, Voiced: true})
	r := s.Finish()
	if r.Score < 0 || r.Score > 10000 || math.IsNaN(r.MeanAbsCents) || math.IsNaN(r.MeanSignedCents) {
		t.Errorf("result = %+v", r)
	}
}

func FuzzScoreIsAlwaysBoundedAndFinite(f *testing.F) {
	f.Add([]byte{40, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20})
	f.Add([]byte{255, 0, 255, 255, 255, 3, 1, 1, 200, 1, 1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 {
			return
		}
		n := 1 + int(data[0])*3
		ref := reference.Grid{Step: step, MIDI: make([]float64, n), Voiced: make([]bool, n)}
		for i := range ref.MIDI {
			b := data[(1+i)%len(data)]
			ref.MIDI[i], ref.Voiced[i] = 30+float64(b)/4, b&1 == 1
		}
		var lines []lyrics.Line
		if data[1]%2 == 0 {
			lines = []lyrics.Line{
				{Start: 0, End: time.Duration(data[2]) * 30 * time.Millisecond, Text: "alpha"},
				{Start: time.Duration(data[2]) * 30 * time.Millisecond, End: time.Duration(n) * step, Text: "bravo"},
			}
		}
		s := New(ref, lines, Config{Difficulty: Difficulty(data[1] % 3), Slack: time.Duration(data[3]) * time.Millisecond})
		for i := 4; i+2 < len(data); i += 3 {
			midi := 30 + float64(data[i+1])/2
			if data[i+2] == 0xff {
				midi = math.NaN()
			}
			s.Push(Frame{T: time.Duration(data[i]) * 13 * time.Millisecond, MIDI: midi, Voiced: data[i+2]&1 == 1})
			if sc := s.Snapshot().Score; sc < 0 || sc > 10000 {
				t.Fatalf("live score %d out of range", sc)
			}
		}
		r := s.Finish()
		if r.Score < 0 || r.Score > 10000 {
			t.Fatalf("score %d out of range", r.Score)
		}
		for _, v := range []float64{r.NotePoints, r.LineBonus, r.InTunePercent, r.MeanAbsCents, r.MeanSignedCents} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite stat in %+v", r)
			}
		}
		for _, l := range r.Lines {
			if math.IsNaN(l.Ratio) || l.Ratio < 0 || l.Ratio > 1 {
				t.Fatalf("bad line ratio %+v", l)
			}
		}
	})
}
