// Package ultrastar exports a prepared karaoke song as an UltraStar Deluxe
// song folder: a .txt chart (format 1.1.0) plus the audio it references.
//
// The chart is derived from the original singer's pitch contour, so the game
// plays back what the karaoke session itself scores against.
package ultrastar

import (
	"math"
	"sort"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/reference"
)

// c4MIDI is the MIDI number UltraStar calls pitch 0.
const c4MIDI = 60

// SegmentConfig holds the thresholds that turn a pitch contour into notes.
type SegmentConfig struct {
	// MedianFrames is the width of the median filter applied to voiced
	// frames. It removes single-frame octave errors and spikes without
	// smearing a real step the way a mean would.
	MedianFrames int
	// JumpSemitones is how far a frame must sit from the running note median
	// to count as a departure. Vibrato up to about +-40 cents stays below
	// it, so a wobbling held note is not chopped into several.
	JumpSemitones float64
	// JumpHold is how long a departure must persist before it starts a new
	// note. Without it a brief slide or a glitch would split a note.
	JumpHold time.Duration
	// MinNote drops notes shorter than this: nobody can sing them, and the
	// game would show them as noise.
	MinNote time.Duration
	// MergeGap joins adjacent notes of the same pitch separated by less than
	// this, since a tracker dropout inside one sung note is not a rest.
	MergeGap time.Duration
}

// DefaultSegmentConfig returns the tuned defaults.
func DefaultSegmentConfig() SegmentConfig {
	return SegmentConfig{
		MedianFrames:  5,
		JumpSemitones: 0.7,
		JumpHold:      50 * time.Millisecond,
		MinNote:       80 * time.Millisecond,
		MergeGap:      40 * time.Millisecond,
	}
}

// Note is a sung note in seconds-space, before conversion to beats.
type Note struct {
	Start, End time.Duration
	// Pitch is semitones relative to C4 (UltraStar's 0); negative is below.
	Pitch int
}

// Segment turns a gated pitch grid into notes, ordered and non-overlapping.
func Segment(g reference.Grid, cfg SegmentConfig) []Note {
	if g.Step <= 0 || len(g.MIDI) == 0 {
		return nil
	}
	if cfg.MedianFrames < 1 {
		cfg.MedianFrames = 1
	}
	holdFrames := int((cfg.JumpHold + g.Step - 1) / g.Step)
	if holdFrames < 1 {
		holdFrames = 1
	}

	var notes []Note
	n := len(g.MIDI)
	for i := 0; i < n; {
		if !g.Voiced[i] {
			i++
			continue
		}
		j := i
		for j < n && g.Voiced[j] {
			j++
		}
		filtered := medianFilter(g.MIDI[i:j], cfg.MedianFrames)
		for _, r := range splitSpan(filtered, cfg.JumpSemitones, holdFrames) {
			notes = append(notes, Note{
				Start: time.Duration(i+r.from) * g.Step,
				End:   time.Duration(i+r.to) * g.Step,
				Pitch: int(math.Round(r.median)) - c4MIDI,
			})
		}
		i = j
	}

	kept := notes[:0]
	for _, nt := range notes {
		if nt.End-nt.Start >= cfg.MinNote {
			kept = append(kept, nt)
		}
	}
	return mergeNotes(kept, cfg.MergeGap)
}

// mergeNotes joins same-pitch neighbours closer than gap.
func mergeNotes(notes []Note, gap time.Duration) []Note {
	var out []Note
	for _, nt := range notes {
		if k := len(out) - 1; k >= 0 && out[k].Pitch == nt.Pitch && nt.Start-out[k].End < gap {
			out[k].End = nt.End
			continue
		}
		out = append(out, nt)
	}
	return out
}

type frameRun struct {
	from, to int // [from, to)
	median   float64
}

// splitSpan cuts one voiced span into notes. A new note starts when the pitch
// has stayed at least jump semitones from the current note's median for
// holdFrames frames in a row; the note then begins where the departure did.
// Departures that end sooner are folded back into the note.
func splitSpan(m []float64, jump float64, holdFrames int) []frameRun {
	var (
		runs   []frameRun
		start  int
		vals   []float64 // frames of the current note
		depart int       // consecutive departed frames pending
	)
	for k, v := range m {
		if len(vals) > 0 && math.Abs(v-median(vals)) >= jump {
			depart++
			if depart >= holdFrames {
				cut := k - depart + 1
				runs = append(runs, frameRun{start, cut, median(vals)})
				start = cut
				vals = append([]float64(nil), m[cut:k+1]...)
				depart = 0
			}
			continue
		}
		vals = append(vals, m[k-depart:k+1]...)
		depart = 0
	}
	return append(runs, frameRun{start, len(m), median(vals)})
}

// medianFilter smooths m with a window of width w, clipped at the ends of m so
// it never mixes in frames from another voiced span.
func medianFilter(m []float64, w int) []float64 {
	out := make([]float64, len(m))
	half := w / 2
	for i := range m {
		lo, hi := max(0, i-half), min(len(m), i+half+1)
		out[i] = median(m[lo:hi])
	}
	return out
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Timing maps between seconds and UltraStar beats.
type Timing struct {
	// BPM is the value written to #BPM. UltraStar's BPM counts quarter notes,
	// so it is four times the song's own tempo.
	BPM float64
	// GapMS is #GAP: the delay of beat 0 in milliseconds.
	GapMS float64
}

// DefaultTiming picks one beat of 10 ms: 60 / (1500 * 4) s. That matches the
// scorer's 10 ms grid, so quantizing to beats loses at most 5 ms per edge.
// A tempo this high is unusual (songs sit near 100-400); if USDX turns out to
// mishandle it, lower BPM to 750 (20 ms beats), which only coarsens rounding.
func DefaultTiming() Timing { return Timing{BPM: 1500, GapMS: 0} }

// Seconds is the song time of a beat: GAP/1000 + beat * 60 / (BPM * 4).
//
// This is our reading of the usdx.eu 1.1.0 spec, which gives no explicit
// formula ("UltraStar BPM is quarter notes, thus it's quadruple of the song's
// BPM"; GAP is the delay of beat 0). It is the interpretation to verify by
// loading an export in UltraStar Deluxe and checking the notes line up with
// the audio. Every conversion goes through here, so correcting the factor is a
// one-line change.
func (t Timing) Seconds(beat float64) float64 {
	return t.GapMS/1000 + beat*60/(t.BPM*4)
}

// Beat is the inverse of Seconds.
func (t Timing) Beat(seconds float64) float64 {
	return (seconds - t.GapMS/1000) / (t.Seconds(1) - t.Seconds(0))
}

// ChartNote is a note on the beat grid.
type ChartNote struct {
	Beat, Length, Pitch int
	// Text is the syllable; empty until lyrics are mapped.
	Text string
	// Start and End are the note's original times, kept for lyric mapping
	// and phrase breaks, which are decided in seconds.
	Start, End time.Duration
}

// ToChart quantizes notes to beats. Each edge rounds half away from zero,
// lengths are at least one beat, and notes never overlap: a note that would
// start inside its predecessor is pushed to the predecessor's end.
func ToChart(notes []Note, t Timing) []ChartNote {
	out := make([]ChartNote, 0, len(notes))
	prevEnd := math.MinInt
	for _, nt := range notes {
		start := int(math.Round(t.Beat(nt.Start.Seconds())))
		end := int(math.Round(t.Beat(nt.End.Seconds())))
		start = max(start, prevEnd)
		end = max(end, start+1)
		out = append(out, ChartNote{
			Beat: start, Length: end - start, Pitch: nt.Pitch,
			Start: nt.Start, End: nt.End,
		})
		prevEnd = end
	}
	return out
}
