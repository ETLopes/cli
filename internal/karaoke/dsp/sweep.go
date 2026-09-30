package dsp

import (
	"fmt"
	"math"
)

// Sweep is an exponential sine sweep and its inverse filter, the measurement
// signal of Farina ("Simultaneous measurement of impulse response and
// distortion with a swept-sine technique", AES 108th Convention, 2000).
//
// Played through the speakers and recorded by a microphone, the sweep lets one
// solve for the room's impulse response by convolving the recording with the
// inverse filter. Compared with a noise or MLS excitation, the exponential
// sweep spends equal time per octave (a good signal-to-noise ratio at the low
// frequencies where rooms are worst), and, decisively for a loudspeaker
// chain, it maps each harmonic distortion product to a separate time before
// the linear impulse response, so the nonlinearity of a cheap speaker does not
// pollute the linear response the echo canceller is seeded with.
type Sweep struct {
	f1, f2, rate float64
	signal       []float64
	inverse      []float64
}

// NewSweep builds a sweep from f1 to f2 Hz lasting seconds at the given sample
// rate. The ends are faded over 5 ms to avoid clicks.
func NewSweep(f1, f2, seconds, rate float64) (*Sweep, error) {
	switch {
	case rate <= 0 || seconds <= 0:
		return nil, fmt.Errorf("sweep needs a positive rate and duration, got %v Hz and %v s", rate, seconds)
	case f1 <= 0 || f2 <= f1:
		return nil, fmt.Errorf("sweep range %v-%v Hz is not valid", f1, f2)
	case f2 >= rate/2:
		return nil, fmt.Errorf("sweep end %v Hz is not below Nyquist (%v Hz)", f2, rate/2)
	}
	n := int(math.Round(seconds * rate))
	if n < 64 {
		return nil, fmt.Errorf("sweep of %v s at %v Hz is only %d samples", seconds, rate, n)
	}
	s := &Sweep{f1: f1, f2: f2, rate: rate, signal: make([]float64, n), inverse: make([]float64, n)}
	dur := float64(n) / rate
	ratio := math.Log(f2 / f1)
	k := 2 * math.Pi * f1 * dur / ratio
	fade := int(0.005 * rate)
	for i := range s.signal {
		t := float64(i) / rate
		v := math.Sin(k * (math.Exp(t/dur*ratio) - 1))
		if i < fade {
			v *= 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(fade))
		} else if j := n - 1 - i; j < fade {
			v *= 0.5 - 0.5*math.Cos(math.Pi*float64(j)/float64(fade))
		}
		s.signal[i] = v
	}
	// Inverse filter: the sweep reversed in time with an envelope falling
	// 6 dB per octave, which flattens the sweep's pink (1/f) spectrum so that
	// sweep * inverse is a band-limited impulse.
	for i := range s.inverse {
		t := float64(i) / rate
		s.inverse[i] = s.signal[n-1-i] * math.Exp(-t/dur*ratio)
	}
	// Normalise so the sweep convolved with its own inverse peaks at one.
	self := fftConvolve(s.signal, s.inverse)
	peak := 0.0
	for _, v := range self {
		peak = math.Max(peak, math.Abs(v))
	}
	for i := range s.inverse {
		s.inverse[i] /= peak
	}
	return s, nil
}

// Signal is the samples to play.
func (s *Sweep) Signal() []float32 { return f32(s.signal) }

// Len is the sweep length in samples.
func (s *Sweep) Len() int { return len(s.signal) }

// ImpulseResponse deconvolves a recording of the played sweep into the
// impulse response of the path it travelled, returning length samples starting
// at the moment the sweep began playing (so the bulk delay is preserved as
// leading time). The recording should extend at least length samples past the
// end of the sweep or the tail of the response is truncated.
func (s *Sweep) ImpulseResponse(recording []float32, length int) ([]float32, error) {
	if length < 1 {
		return nil, fmt.Errorf("impulse response length %d must be positive", length)
	}
	if len(recording) < len(s.signal) {
		return nil, fmt.Errorf("recording of %d samples is shorter than the %d-sample sweep", len(recording), len(s.signal))
	}
	rec := make([]float64, len(recording))
	for i, v := range recording {
		rec[i] = float64(v)
	}
	full := fftConvolve(rec, s.inverse)
	// Sample len-1 of the full convolution is time zero of the response;
	// distortion products sit before it and are simply not read.
	origin := len(s.signal) - 1
	out := make([]float32, length)
	for i := range out {
		if origin+i < len(full) {
			out[i] = float32(full[origin+i])
		}
	}
	return out, nil
}

// EchoTail analyses a measured impulse response. onset is the sample index of
// the direct sound (the largest absolute value). tail is the time in seconds,
// counted from onset, for the Schroeder backward-integrated energy decay
// curve to fall to -40 dB: how long the filter that cancels this path must be.
//
// Schroeder integration (1965) sums the squared response from the end
// backwards, which turns the noisy, fluctuating decay of a single measurement
// into a smooth monotonic curve. A measured response ends in a noise floor
// that would keep the curve from ever decaying, so the floor power, taken as
// the mean of the last tenth of the response, is subtracted first. -40 dB
// rather than the full -60 dB of RT60 because a 60 dB decay is rarely above
// the noise of a real measurement, and 40 dB of remaining tail is more than the
// 20 dB to 30 dB of echo suppression the canceller is asked for.
func EchoTail(ir []float32, rate float64) (onset int, tail float64) {
	if len(ir) < 10 || rate <= 0 {
		return 0, 0
	}
	for i, v := range ir {
		if math.Abs(float64(v)) > math.Abs(float64(ir[onset])) {
			onset = i
		}
	}
	end := len(ir)
	var floor float64
	for _, v := range ir[end-end/10:] {
		floor += float64(v) * float64(v)
	}
	floor /= float64(end / 10)
	energy := make([]float64, end-onset+1) // energy[i] = remaining energy from onset+i
	for i := end - 1; i >= onset; i-- {
		e := float64(ir[i])*float64(ir[i]) - floor
		energy[i-onset] = energy[i-onset+1] + math.Max(0, e)
	}
	if energy[0] <= 0 {
		return onset, 0
	}
	target := energy[0] * 1e-4
	for i, e := range energy {
		if e <= target {
			return onset, float64(i) / rate
		}
	}
	return onset, float64(end-onset) / rate
}
