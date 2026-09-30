// Package score judges a singer against the reference pitch contour.
//
// It follows UltraStar Deluxe's scoring in spirit: octave folding to within
// six semitones, a semitone tolerance per difficulty, 9000 note points plus
// 1000 line bonus. It diverges where the data differs. USDX compares against
// segmented notes; we compare against the continuous contour, frame by frame,
// so automatic note segmentation errors cannot become scoring errors. USDX's
// line-bonus formula was not verified, so ours is our own (see LineBonus).
package score

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
)

const (
	// NotePoints are shared equally by the voiced reference frames.
	NotePoints = 9000.0
	// LineBonus is shared by the lines in proportion to their voiced frames,
	// and each line earns its share times its hit ratio.
	LineBonus = 1000.0
	// MaxScore is the ceiling of the scale.
	MaxScore = NotePoints + LineBonus

	// DefaultSlack is the timing slack either side of a reference frame.
	DefaultSlack = 100 * time.Millisecond
	// FallbackLineWindow stands in for lines when there are no lyrics.
	FallbackLineWindow = 4 * time.Second

	// tolerance is compared with a hair of float slack so that a sung value
	// exactly on the boundary is a hit despite representation error.
	toleranceEpsilon = 1e-9
)

// Difficulty selects how far from the target a hit may be.
type Difficulty int

const (
	Easy Difficulty = iota
	Medium
	Hard
)

// Tolerance returns the largest |sung - target| in semitones that is a hit.
// It is the continuous equivalent of USDX's rule |round(sung)-note| <= 2-level
// on rounded tones, which admits anything under half a semitone more. An
// unknown value gets the Medium tolerance.
func (d Difficulty) Tolerance() float64 {
	switch d {
	case Easy:
		return 2.5
	case Hard:
		return 0.5
	default:
		return 1.5
	}
}

// String returns the config spelling.
func (d Difficulty) String() string {
	switch d {
	case Easy:
		return "easy"
	case Hard:
		return "hard"
	default:
		return "medium"
	}
}

// ParseDifficulty reads a config value, ignoring case and surrounding space.
func ParseDifficulty(s string) (Difficulty, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "easy":
		return Easy, nil
	case "medium":
		return Medium, nil
	case "hard":
		return Hard, nil
	}
	return Medium, fmt.Errorf("unknown difficulty %q (want easy, medium or hard)", s)
}

// Config tunes a scorer.
type Config struct {
	Difficulty Difficulty
	// Slack is how far either side of a reference frame a sung frame may be
	// and still count. Zero or negative means DefaultSlack.
	Slack time.Duration
}

// Frame is one player pitch estimate. T is song time, already compensated for
// latency by the caller.
type Frame struct {
	T      time.Duration
	MIDI   float64
	Voiced bool
}

// Event is the outcome of the latest finalized voiced reference frame.
type Event int

const (
	EventNone Event = iota
	EventHit
	EventMiss
)

// Snapshot is the cheap live view the UI reads.
type Snapshot struct {
	// Score is the running total; it only ever grows.
	Score int
	// Streak is the current run of hit frames.
	Streak time.Duration
	// Last is whether the latest finalized voiced frame was a hit or a miss.
	Last Event
	// Time is the latest player time seen.
	Time time.Duration
	// TargetMIDI is the reference pitch at Time, meaningful when
	// TargetVoiced is set.
	TargetMIDI   float64
	TargetVoiced bool
}

// Scorer scores ONE player. Push and Snapshot may be called from different
// goroutines.
//
// A reference frame at t is finalized once player time has passed t+slack,
// so the score is monotonic. A sung frame that arrives later than that, for
// a reference frame already finalized, is too late to count; pushing frames
// in roughly time order (within one Push call any order is fine) gives the
// same result as pushing them all at once.
type Scorer struct {
	mu sync.Mutex

	ref   reference.Grid
	n     int
	tol   float64
	slack time.Duration

	voiced []bool // per reference frame, finite and voiced
	lineOf []int  // per reference frame, index into tally.lines; -1 if unvoiced
	tally  tally

	frames []Frame // voiced player frames, sorted by T, older than the window trimmed
	latest time.Duration
	next   int // first reference frame not yet finalized

	last   Event
	done   bool
	result Result
}

// New returns a scorer for one player.
//
// Lines come from the non-blank lyric lines; a voiced reference frame outside
// every line joins the nearest one. Without lyrics, fixed 4 s windows play
// the part of lines. A reference with no voiced frames scores 0 (there is
// nothing to be in tune with), and its stats are all zero.
func New(ref reference.Grid, lines []lyrics.Line, cfg Config) *Scorer {
	s := &Scorer{ref: ref, tol: cfg.Difficulty.Tolerance(), slack: cfg.Slack}
	if s.slack <= 0 {
		s.slack = DefaultSlack
	}
	if ref.Step > 0 {
		s.n = min(len(ref.MIDI), len(ref.Voiced))
	}
	s.voiced = make([]bool, s.n)
	for i := range s.voiced {
		s.voiced[i] = ref.Voiced[i] && isFinite(ref.MIDI[i])
	}
	s.lineOf, s.tally = assignLines(s.voiced, ref.Step, lines)
	return s
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

type lineSpan struct {
	index      int
	start, end time.Duration
}

// assignLines maps each voiced reference frame to a line and counts them.
func assignLines(voiced []bool, step time.Duration, lines []lyrics.Line) ([]int, tally) {
	var cands []lineSpan
	for i, l := range lines {
		if !l.Blank() {
			cands = append(cands, lineSpan{i, l.Start, l.End})
		}
	}

	raw := make([]int, len(voiced))
	counts := map[int]int{}
	for i, v := range voiced {
		raw[i] = -1
		if !v {
			continue
		}
		t := time.Duration(i) * step
		if len(cands) == 0 {
			raw[i] = int(t / FallbackLineWindow)
		} else {
			raw[i] = nearest(cands, t)
		}
		counts[raw[i]]++
	}

	indexes := make([]int, 0, len(counts))
	total := 0
	for idx, c := range counts {
		indexes = append(indexes, idx)
		total += c
	}
	sort.Ints(indexes)
	pos := make(map[int]int, len(indexes))
	tl := tally{step: step, voiced: total}
	for p, idx := range indexes {
		pos[idx] = p
		tl.lines = append(tl.lines, lineTally{index: idx, voiced: counts[idx], remaining: counts[idx]})
	}
	lineOf := make([]int, len(voiced))
	for i, r := range raw {
		lineOf[i] = -1
		if r >= 0 {
			lineOf[i] = pos[r]
		}
	}
	return lineOf, tl
}

// nearest returns the original index of the line containing t, or failing
// that the one whose span is closest; the earliest wins a tie.
func nearest(cands []lineSpan, t time.Duration) int {
	best, bestDist := -1, time.Duration(math.MaxInt64)
	for _, c := range cands {
		var d time.Duration
		switch {
		case t < c.start:
			d = c.start - t
		case t >= c.end:
			d = t - c.end + 1 // End is exclusive, so a frame at End is outside
		}
		if d < bestDist {
			best, bestDist = c.index, d
		}
	}
	return best
}

// Push adds player frames. Frames may be unordered, duplicated or gapped.
func (s *Scorer) Push(frames ...Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	if !framesSorted(frames) {
		frames = sortedCopy(frames)
	}
	for _, f := range frames {
		s.latest = max(s.latest, f.T)
		if f.Voiced && isFinite(f.MIDI) {
			s.insert(f)
		}
	}
	s.advance(false)
}

// insert keeps s.frames sorted by time and drops exact duplicates. Frames too
// old to matter to any unfinalized reference frame are ignored.
func (s *Scorer) insert(f Frame) {
	if f.T < s.refTime(s.next)-s.slack {
		return
	}
	at := len(s.frames)
	if at > 0 && s.frames[at-1].T > f.T {
		at = sort.Search(len(s.frames), func(i int) bool { return s.frames[i].T > f.T })
	}
	for j := at - 1; j >= 0 && s.frames[j].T == f.T; j-- {
		if s.frames[j].MIDI == f.MIDI {
			return
		}
	}
	s.frames = append(s.frames, Frame{})
	copy(s.frames[at+1:], s.frames[at:])
	s.frames[at] = f
}

func (s *Scorer) refTime(i int) time.Duration { return time.Duration(i) * s.ref.Step }

// advance finalizes reference frames whose window has closed, or all of them.
func (s *Scorer) advance(all bool) {
	for s.next < s.n {
		t := s.refTime(s.next)
		if !all && t+s.slack >= s.latest {
			break
		}
		if s.voiced[s.next] {
			s.finalize(s.next, t)
		}
		s.next++
	}
	cut := sort.Search(len(s.frames), func(i int) bool { return s.frames[i].T >= s.refTime(s.next)-s.slack })
	// Shift down rather than reslice, so the buffer keeps its capacity and
	// steady-state pushes never reallocate.
	s.frames = s.frames[:copy(s.frames, s.frames[cut:])]
}

// sortedCopy is the rare path for unordered input. It is a function of its own
// so that boxing the copy for sort.SliceStable does not make Push's argument
// escape, which would cost every call an allocation.
func sortedCopy(frames []Frame) []Frame {
	out := append([]Frame(nil), frames...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// framesSorted reports whether frames are in time order, without the
// allocation sort.SliceIsSorted makes boxing its argument.
func framesSorted(frames []Frame) bool {
	for i := 1; i < len(frames); i++ {
		if frames[i].T < frames[i-1].T {
			return false
		}
	}
	return true
}

// finalize decides one voiced reference frame from the best sung frame in its
// window.
func (s *Scorer) finalize(i int, t time.Duration) {
	target := s.ref.MIDI[i]
	first := sort.Search(len(s.frames), func(k int) bool { return s.frames[k].T >= t-s.slack })
	var o outcome
	bestAbs := math.Inf(1)
	for _, f := range s.frames[first:] {
		if f.T > t+s.slack {
			break
		}
		d := fold(f.MIDI - target)
		if a := math.Abs(d); a < bestAbs {
			bestAbs, o.cents, o.matched = a, d*100, true
		}
	}
	o.hit = o.matched && bestAbs <= s.tol+toleranceEpsilon
	s.tally.add(s.lineOf[i], o)
	s.last = EventMiss
	if o.hit {
		s.last = EventHit
	}
}

// fold shifts a pitch difference by whole octaves into [-6, 6] semitones, so
// singing an octave away from the singer on the record still counts.
func fold(d float64) float64 { return d - 12*math.Round(d/12) }

// Snapshot returns the live score. It is cheap and safe to call while Push
// runs on another goroutine.
func (s *Scorer) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	note, bonus := s.tally.points()
	snap := Snapshot{
		Score:  totalScore(note, bonus),
		Streak: time.Duration(s.tally.streak) * s.ref.Step,
		Last:   s.last,
		Time:   s.latest,
	}
	if s.done {
		snap.Score = s.result.Score
	}
	if s.n > 0 {
		// Snap to the nearest reference frame, as reference.Grid.At does.
		if i := int((s.latest + s.ref.Step/2) / s.ref.Step); i < s.n {
			snap.TargetMIDI, snap.TargetVoiced = s.ref.MIDI[i], s.voiced[i]
		}
	}
	return snap
}

// Finish finalizes every reference frame and returns the player's result. A
// later call returns the same result and later pushes are ignored.
func (s *Scorer) Finish() Result { return s.finish(false) }

// FinishEarly is Finish for a song stopped before its end. The score counts
// only what was sung so far, out of the full-song denominator: reference
// frames after the last sung frame are misses, so stopping early can never
// score better than singing on. The result is flagged Incomplete.
func (s *Scorer) FinishEarly() Result { return s.finish(true) }

func (s *Scorer) finish(incomplete bool) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.advance(true)
		s.done = true
		s.result = s.tally.result(incomplete)
	}
	return s.result
}
