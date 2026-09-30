package dsp

import (
	"math"
	"math/rand"
)

// This file is the shared synthetic world for the tests: a music-like
// reference, a singer with a known pitch trajectory, and a room that turns the
// one into an echo of itself in the other's microphone. Everything is
// deterministic from a seed so failures reproduce.

const testRate = PipelineRate

// harmonicVoice synthesises a harmonic tone whose fundamental follows f0(t)
// (seconds -> Hz), amplitudes amps[k] for harmonic k+1, and a 1/f-ish tilt
// applied on top of amps when tilt is true (a stand-in for the formant
// roll-off of a voice). The phase is integrated so a moving f0 stays clean.
func harmonicVoice(n int, f0 func(t float64) float64, amps []float64, tilt bool) []float64 {
	out := make([]float64, n)
	phase := 0.0
	for i := range out {
		t := float64(i) / testRate
		f := f0(t)
		phase += 2 * math.Pi * f / testRate
		for k, a := range amps {
			h := float64(k + 1)
			if h*f >= testRate/2*0.95 {
				break
			}
			if tilt {
				a /= math.Sqrt(h)
			}
			out[i] += a * math.Sin(h*phase)
		}
	}
	return out
}

func constF0(f float64) func(float64) float64 { return func(float64) float64 { return f } }

// vibratoF0 oscillates +-depthCents around centre at rateHz.
func vibratoF0(centre, depthCents, rateHz float64) func(float64) float64 {
	return func(t float64) float64 {
		return centre * math.Pow(2, depthCents/1200*math.Sin(2*math.Pi*rateHz*t))
	}
}

// singerLine is a sung melody: a sequence of notes with short gaps, vibrato on
// the long ones. truth[i] is the f0 at sample i, 0 where the singer is silent.
func singerLine(n int, level float64, seed int64) (signal, truth []float64) {
	rng := rand.New(rand.NewSource(seed))
	signal = make([]float64, n)
	truth = make([]float64, n)
	notes := []float64{220, 247, 294, 262, 330, 196, 262, 349}
	pos := int(0.1 * testRate)
	for ni := 0; pos < n; ni++ {
		f := notes[ni%len(notes)] * (1 + 0.002*rng.NormFloat64())
		length := int((0.45 + 0.2*rng.Float64()) * testRate)
		if pos+length > n {
			length = n - pos
		}
		vib := vibratoF0(f, 25, 5.2)
		note := harmonicVoice(length, func(t float64) float64 {
			// Vibrato fades in over the first 150 ms of the note.
			depth := math.Min(1, t/0.15)
			return f * math.Pow(vib(t)/f, depth)
		}, []float64{1, 0.8, 0.6, 0.35, 0.2, 0.12, 0.06}, true)
		for i, v := range note {
			// Short attack and release avoid clicks.
			env := math.Min(1, math.Min(float64(i)/200, float64(length-i)/300))
			signal[pos+i] = level * env * v
			t := float64(i) / testRate
			depth := math.Min(1, t/0.15)
			truth[pos+i] = f * math.Pow(vib(t)/f, depth)
		}
		pos += length + int((0.08+0.1*rng.Float64())*testRate)
	}
	return signal, truth
}
