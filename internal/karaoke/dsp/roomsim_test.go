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

// musicReference is a stand-in for a backing track: chords of harmonic notes
// that change every 0.6 s, a kick and a noisy hi-hat on a 0.3 s pulse. It is
// deliberately not white: a tonal, strongly correlated reference with spectral
// gaps is what makes a real echo canceller hard, and white noise alone would
// flatter it. Scaled to 0.1 RMS (-20 dBFS).
func musicReference(n int, seed int64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, n)
	roots := [][]float64{{130.8, 164.8, 196}, {110, 138.6, 164.8}, {146.8, 174.6, 220}, {98, 123.5, 146.8}}
	chordLen := int(0.6 * testRate)
	for pos := 0; pos < n; pos += chordLen {
		chord := roots[rng.Intn(len(roots))]
		length := min(chordLen+1600, n-pos)
		for _, f := range chord {
			f *= math.Pow(2, float64(rng.Intn(2)))
			note := harmonicVoice(length, constF0(f), []float64{1, 0.5, 0.33, 0.25, 0.2, 0.16}, false)
			for i, v := range note {
				t := float64(i) / testRate
				env := math.Min(1, t/0.02) * math.Exp(-t/0.5) * math.Min(1, float64(length-i)/800)
				out[pos+i] += 0.3 * env * v
			}
		}
	}
	beat := int(0.3 * testRate)
	for pos, k := 0, 0; pos < n; pos, k = pos+beat, k+1 {
		if k%2 == 0 { // kick: falling sine
			phase := 0.0
			for i := 0; i < int(0.15*testRate) && pos+i < n; i++ {
				t := float64(i) / testRate
				phase += 2 * math.Pi * (50 + 90*math.Exp(-t/0.03)) / testRate
				out[pos+i] += 0.8 * math.Exp(-t/0.06) * math.Sin(phase)
			}
		}
		prev := 0.0
		for i := 0; i < int(0.08*testRate) && pos+beat/2+i < n; i++ { // hat: differentiated noise
			t := float64(i) / testRate
			w := rng.NormFloat64()
			out[pos+beat/2+i] += 0.25 * math.Exp(-t/0.02) * (w - prev)
			prev = w
		}
	}
	return scaleToRMS(out, 0.1)
}

func rms(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

func scaleToRMS(x []float64, target float64) []float64 {
	g := target / rms(x)
	for i := range x {
		x[i] *= g
	}
	return x
}

func whiteNoise(n int, sigma float64, seed int64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, n)
	for i := range out {
		out[i] = sigma * rng.NormFloat64()
	}
	return out
}

// roomConfig describes a synthetic speaker-to-microphone path.
type roomConfig struct {
	bulkDelay int     // samples before the direct sound arrives
	rt60      float64 // seconds for the reverberant tail to decay by 60 dB
	gain      float64 // amplitude of the direct path; 0 means 0.5
	seed      int64
}

// room is a fixed impulse response: bulk delay, direct sound, a handful of
// early reflections, then an exponentially decaying noise tail.
type room struct {
	ir []float64
}

func newRoom(cfg roomConfig) *room {
	if cfg.gain == 0 {
		cfg.gain = 0.5
	}
	rng := rand.New(rand.NewSource(cfg.seed))
	tail := int(1.2 * cfg.rt60 * testRate)
	ir := make([]float64, cfg.bulkDelay+tail+int(0.03*testRate)+1)
	ir[cfg.bulkDelay] = cfg.gain
	for k := 0; k < 8; k++ { // early reflections within 25 ms
		d := cfg.bulkDelay + 8 + rng.Intn(int(0.025*testRate))
		ir[d] += cfg.gain * (0.15 + 0.3*rng.Float64()) * float64(1-2*rng.Intn(2))
	}
	start := cfg.bulkDelay + int(0.01*testRate)
	for i := start; i < len(ir); i++ {
		t := float64(i-start) / testRate
		ir[i] += cfg.gain * 0.25 * math.Exp(-6.9078*t/cfg.rt60) * rng.NormFloat64()
	}
	return &room{ir: ir}
}

// apply convolves x with the room, returning len(x) samples.
func (r *room) apply(x []float64) []float64 { return fftConvolve(x, r.ir)[:len(x)] }

// fftConvolve is a full linear convolution through one large FFT.
func fftConvolve(x, h []float64) []float64 {
	n := 1
	for n < len(x)+len(h) {
		n <<= 1
	}
	f, _ := NewFFT(n)
	a, b := make([]complex128, n), make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v, 0)
	}
	for i, v := range h {
		b[i] = complex(v, 0)
	}
	f.Forward(a)
	f.Forward(b)
	for i := range a {
		a[i] *= b[i]
	}
	f.Inverse(a)
	out := make([]float64, len(x)+len(h)-1)
	for i := range out {
		out[i] = real(a[i])
	}
	return out
}

// softClip is the mild loudspeaker nonlinearity: identity for small signals,
// compressive for peaks. drive of 0 disables it.
func softClip(x []float64, drive float64) []float64 {
	if drive == 0 {
		return x
	}
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = math.Tanh(drive*v) / drive
	}
	return out
}

// dB helpers over sample ranges.
func powerDB(x []float64) float64 { return 10 * math.Log10(rms(x)*rms(x)+1e-30) }

// erleDB is the echo return loss enhancement between what was in the
// microphone (echo only) and what the canceller left, over [from, to).
func erleDB(echo, out []float64, from, to int) float64 {
	return powerDB(echo[from:to]) - powerDB(out[from:to])
}

func sumSignals(parts ...[]float64) []float64 {
	out := make([]float64, len(parts[0]))
	for _, p := range parts {
		for i, v := range p {
			out[i] += v
		}
	}
	return out
}
