// Package reference holds the original singer's pitch contour: the yardstick a
// live performance is scored against. It defines the on-disk format, the
// voicing gates that decide which frames count as singing, and a resampler onto
// the scorer's 10 ms grid. Producing a contour is the Extractor's job.
package reference

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// Extractor turns a vocal WAV into a contour file. It is an interface so a
// second pitch tracker (RMVPE, say) can replace SwiftF0 if SwiftF0 proves
// noisy on separated vocals, without the song pipeline noticing.
type Extractor interface {
	// Extract analyses the WAV at wav and writes a contour JSON file at out.
	Extract(ctx context.Context, wav, out string) error
}

// Gates decide which detector frames count as the singer actually singing.
// The tracker reports a pitch for nearly every frame; without gating, breaths
// and Demucs bleed of the instruments would become notes the player must hit.
type Gates struct {
	// MinConfidence is the detector confidence a frame needs.
	MinConfidence float64
	// MinLoudnessDB is the RMS floor in dBFS. Quiet frames in a separated
	// vocal stem are usually instrument residue, which the detector can be
	// confident about.
	MinLoudnessDB float64
	// MinRun drops voiced runs shorter than this. A blip of a few frames is
	// too short to sing and would only add noise to the score.
	MinRun time.Duration
}

// DefaultGates are the gates Load applies: SwiftF0's own voicing threshold, a
// floor low enough to keep soft singing, and about three frames of minimum run.
func DefaultGates() Gates {
	return Gates{MinConfidence: 0.5, MinLoudnessDB: -45, MinRun: 50 * time.Millisecond}
}

// Contour is a pitch track: one entry per analysis frame in every slice. The
// exported fields are the JSON format written by the extractor script.
type Contour struct {
	// HopSeconds is the spacing between frames (16 ms for SwiftF0).
	HopSeconds float64 `json:"hop"`
	// SampleRate is the rate of the audio that was analysed.
	SampleRate int `json:"sample_rate"`
	// Time is each frame's timestamp in seconds.
	Time       []float64 `json:"time"`
	Hz         []float64 `json:"hz"`
	Confidence []float64 `json:"confidence"`
	LoudnessDB []float64 `json:"loudness_db"`

	// voiced is the result of the gates. It is derived, so it is not stored:
	// the thresholds can then be retuned without re-running the extractor.
	voiced []bool
}

// Len is the number of frames.
func (c *Contour) Len() int { return len(c.Time) }

// Hop is the frame spacing as a duration.
func (c *Contour) Hop() time.Duration {
	return time.Duration(c.HopSeconds * float64(time.Second))
}

// HzToMIDI converts a frequency to a fractional MIDI note number, where 440 Hz
// is 69. Fractions matter: scoring works in continuous semitones, not notes.
func HzToMIDI(hz float64) float64 { return 69 + 12*math.Log2(hz/440) }

// MIDI is frame i's pitch as a fractional MIDI note, or NaN when the frame has
// no pitch.
func (c *Contour) MIDI(i int) float64 {
	if i < 0 || i >= len(c.Hz) || c.Hz[i] <= 0 {
		return math.NaN()
	}
	return HzToMIDI(c.Hz[i])
}

// Voiced reports whether frame i survived the gates. Out-of-range is unvoiced.
func (c *Contour) Voiced(i int) bool {
	return i >= 0 && i < len(c.voiced) && c.voiced[i]
}

// WithGates applies g and returns the contour for chaining. It replaces any
// earlier gating.
func (c *Contour) WithGates(g Gates) *Contour {
	n := c.Len()
	voiced := make([]bool, n)
	for i := 0; i < n; i++ {
		voiced[i] = c.Hz[i] > 0 &&
			c.Confidence[i] >= g.MinConfidence &&
			c.LoudnessDB[i] >= g.MinLoudnessDB
	}

	if g.MinRun > 0 {
		hop := c.Hop()
		for i := 0; i < n; {
			if !voiced[i] {
				i++
				continue
			}
			j := i
			for j < n && voiced[j] {
				j++
			}
			if time.Duration(j-i)*hop < g.MinRun {
				for k := i; k < j; k++ {
					voiced[k] = false
				}
			}
			i = j
		}
	}
	c.voiced = voiced
	return c
}

// validate checks the slices agree in length, which every accessor relies on.
func (c *Contour) validate() error {
	n := len(c.Time)
	if len(c.Hz) != n || len(c.Confidence) != n || len(c.LoudnessDB) != n {
		return fmt.Errorf("contour arrays disagree in length (time %d, hz %d, confidence %d, loudness_db %d)",
			n, len(c.Hz), len(c.Confidence), len(c.LoudnessDB))
	}
	if n > 1 && c.HopSeconds <= 0 {
		return fmt.Errorf("contour has %d frames but hop %v", n, c.HopSeconds)
	}
	return nil
}

// Save writes the contour as JSON.
func (c *Contour) Save(path string) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding contour: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing contour: %w", err)
	}
	return nil
}

// Load reads a contour file and applies DefaultGates.
func Load(path string) (*Contour, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading contour: %w", err)
	}
	var c Contour
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing contour %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("contour %s: %w", path, err)
	}
	return c.WithGates(DefaultGates()), nil
}

// Grid is a contour resampled to a fixed step. The scorer compares the singer
// every 10 ms, whereas SwiftF0 frames arrive every 16 ms.
type Grid struct {
	Step time.Duration
	// MIDI holds the fractional note at each grid point; meaningless where
	// Voiced is false.
	MIDI   []float64
	Voiced []bool
}

// At returns the pitch at time t and whether the singer should be singing then.
// It snaps to the nearest grid point.
func (g Grid) At(t time.Duration) (float64, bool) {
	if t < 0 || g.Step <= 0 {
		return 0, false
	}
	i := int((t + g.Step/2) / g.Step)
	if i >= len(g.MIDI) {
		return 0, false
	}
	return g.MIDI[i], g.Voiced[i]
}

// timeEpsilon absorbs float error when a grid point lands on a frame time.
const timeEpsilon = 1e-9

// Grid resamples onto step-spaced points starting at zero.
//
// Between two voiced frames the MIDI value is interpolated linearly, so the
// target glides as the singer does. It is never interpolated across an
// unvoiced frame: the line between the notes on either side of a rest is not
// a pitch anyone is meant to sing. At the edge of a voiced span the nearest
// voiced frame is held for up to half a hop, so a span loses no time to the
// coarser frame spacing.
func (c *Contour) Grid(step time.Duration) Grid {
	g := Grid{Step: step}
	n := c.Len()
	if n == 0 || step <= 0 {
		return g
	}
	last := c.Time[n-1]
	points := int(last/step.Seconds()+timeEpsilon) + 1
	g.MIDI = make([]float64, points)
	g.Voiced = make([]bool, points)
	halfHop := c.HopSeconds / 2

	for p := 0; p < points; p++ {
		t := float64(p) * step.Seconds()
		// i is the last frame at or before t.
		i := sort.Search(n, func(k int) bool { return c.Time[k] > t+timeEpsilon }) - 1
		if i < 0 {
			continue
		}
		if math.Abs(c.Time[i]-t) <= timeEpsilon {
			if c.Voiced(i) {
				g.MIDI[p], g.Voiced[p] = c.MIDI(i), true
			}
			continue
		}
		j := i + 1
		if j >= n {
			continue
		}
		switch {
		case c.Voiced(i) && c.Voiced(j):
			frac := (t - c.Time[i]) / (c.Time[j] - c.Time[i])
			g.MIDI[p] = c.MIDI(i) + frac*(c.MIDI(j)-c.MIDI(i))
			g.Voiced[p] = true
		case c.Voiced(i) && t-c.Time[i] <= halfHop+timeEpsilon:
			g.MIDI[p], g.Voiced[p] = c.MIDI(i), true
		case c.Voiced(j) && c.Time[j]-t <= halfHop+timeEpsilon:
			g.MIDI[p], g.Voiced[p] = c.MIDI(j), true
		}
	}
	return g
}
