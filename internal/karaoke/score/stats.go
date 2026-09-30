package score

import (
	"math"
	"time"
)

// centsDeadZone is how far the average error may drift before a singer is
// called sharp or flat. Human vibrato and pitch-tracker jitter alone move the
// mean by a few cents, so a tighter zone would label everyone.
const centsDeadZone = 15.0

// Tendency says which way a singer drifts from the target.
type Tendency int

const (
	// TendencyUnknown means no voiced frame was matched, so there is nothing
	// to average. Reporting "centered" for a singer who never sang would
	// flatter them.
	TendencyUnknown Tendency = iota
	TendencyCentered
	TendencySharp
	TendencyFlat
)

// String returns the lowercase name used in the UI catalogue keys.
func (t Tendency) String() string {
	switch t {
	case TendencyCentered:
		return "centered"
	case TendencySharp:
		return "sharp"
	case TendencyFlat:
		return "flat"
	default:
		return "unknown"
	}
}

func classify(meanSignedCents float64, matched int) Tendency {
	switch {
	case matched == 0:
		return TendencyUnknown
	case meanSignedCents > centsDeadZone:
		return TendencySharp
	case meanSignedCents < -centsDeadZone:
		return TendencyFlat
	default:
		return TendencyCentered
	}
}

// LineResult is how one line went. Only lines that hold at least one voiced
// reference frame appear, because a line with nothing to sing has no ratio.
type LineResult struct {
	// Index is the position in the lines given to New (blank lines count), so
	// the UI can map it back to the lyrics. Without lyrics it is the number
	// of the fixed window.
	Index  int
	Voiced int
	Hits   int
	// Ratio is Hits/Voiced.
	Ratio float64
	// Bonus is this line's contribution to the 1000-point line bonus.
	Bonus float64
}

// Result is a player's final performance.
type Result struct {
	// Score is the total, 0..10000, rounded only here.
	Score int
	// NotePoints and LineBonus are the unrounded subtotals; they can sum to
	// Score plus or minus a rounding point.
	NotePoints float64
	LineBonus  float64
	// Incomplete is set when the song was stopped before its end. The
	// denominator is still the whole song, so an early stop cannot look better
	// than finishing.
	Incomplete bool

	VoicedFrames  int
	HitFrames     int
	MatchedFrames int

	// InTunePercent is hit voiced frames over all voiced frames, 0..100.
	InTunePercent float64
	// MeanAbsCents is the mean absolute error of the best matches over hit
	// frames.
	MeanAbsCents float64
	// MeanSignedCents is the mean signed error over all matched voiced frames
	// (positive is sharp, negative is flat).
	MeanSignedCents float64
	Tendency        Tendency

	Lines []LineResult
	// BestLine and WorstLine are LineResult.Index values by hit ratio, the
	// earliest line winning a tie; -1 when no line has voiced frames.
	BestLine  int
	WorstLine int

	// LongestStreak is the longest run of consecutive hit voiced reference
	// frames. Unvoiced reference frames neither break nor extend a run.
	LongestStreak time.Duration
}

// lineTally accumulates one line while the song is scored.
type lineTally struct {
	index     int
	voiced    int
	hits      int
	remaining int
}

// tally accumulates everything the score and the stats derive from. Counts
// are integers and floats are computed on demand, so incremental and batch
// scoring cannot drift apart through summation order.
type tally struct {
	step   time.Duration
	voiced int
	lines  []lineTally // in Index order

	hits        int
	matched     int
	sumAbsCents float64 // over hits
	sumSigned   float64 // over matched
	// completedHits are the hits of lines that are fully finalized; the line
	// bonus is awarded from these.
	completedHits int
	streak        int
	longest       int
}

// outcome is what one finalized voiced reference frame turned out to be.
type outcome struct {
	matched bool
	hit     bool
	// cents is the signed error of the best match, valid when matched.
	cents float64
}

func (t *tally) add(line int, o outcome) {
	l := &t.lines[line]
	if o.matched {
		t.matched++
		t.sumSigned += o.cents
	}
	if o.hit {
		t.hits++
		l.hits++
		t.sumAbsCents += math.Abs(o.cents)
		t.streak++
		t.longest = max(t.longest, t.streak)
	} else {
		t.streak = 0
	}
	l.remaining--
	if l.remaining == 0 {
		t.completedHits += l.hits
	}
}

// points returns the unrounded note points and line bonus.
//
// A line's share of the bonus is 1000*voiced/total and it earns that share
// times its hit ratio, which is 1000*hits/total. Summing over completed lines
// therefore needs only their total hits.
func (t *tally) points() (note, bonus float64) {
	if t.voiced == 0 {
		return 0, 0
	}
	total := float64(t.voiced)
	return NotePoints * float64(t.hits) / total, LineBonus * float64(t.completedHits) / total
}

func totalScore(note, bonus float64) int {
	return int(math.Round(math.Min(note+bonus, MaxScore)))
}

func (t *tally) result(incomplete bool) Result {
	note, bonus := t.points()
	r := Result{
		Score:         totalScore(note, bonus),
		NotePoints:    note,
		LineBonus:     bonus,
		Incomplete:    incomplete,
		VoicedFrames:  t.voiced,
		HitFrames:     t.hits,
		MatchedFrames: t.matched,
		BestLine:      -1,
		WorstLine:     -1,
		LongestStreak: time.Duration(t.longest) * t.step,
	}
	if t.voiced > 0 {
		r.InTunePercent = 100 * float64(t.hits) / float64(t.voiced)
	}
	if t.hits > 0 {
		r.MeanAbsCents = t.sumAbsCents / float64(t.hits)
	}
	if t.matched > 0 {
		r.MeanSignedCents = t.sumSigned / float64(t.matched)
	}
	r.Tendency = classify(r.MeanSignedCents, t.matched)

	var best, worst float64
	for _, l := range t.lines {
		lr := LineResult{
			Index:  l.index,
			Voiced: l.voiced,
			Hits:   l.hits,
			Ratio:  float64(l.hits) / float64(l.voiced),
			Bonus:  LineBonus * float64(l.hits) / float64(t.voiced),
		}
		if len(r.Lines) == 0 || lr.Ratio > best {
			best, r.BestLine = lr.Ratio, lr.Index
		}
		if len(r.Lines) == 0 || lr.Ratio < worst {
			worst, r.WorstLine = lr.Ratio, lr.Index
		}
		r.Lines = append(r.Lines, lr)
	}
	return r
}
