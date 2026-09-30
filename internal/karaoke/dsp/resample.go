package dsp

import (
	"fmt"
	"math"
)

// PipelineRate is the sample rate the whole analysis chain runs at. 16 kHz
// keeps everything a singing voice needs (fundamentals below 1.1 kHz, formants
// below 4 kHz) while making the 500 ms echo filter cost a third of what it
// would at 48 kHz.
const PipelineRate = 16000

const (
	resamplePassEdgeHz = 7000.0 // response stays flat up to here
	resampleStopEdgeHz = 8000.0 // and is at least resampleStopDB down from here
	resampleStopDB     = 70.0   // margin over the 60 dB requirement
	resampleMaxPhases  = 1024   // bounds the prototype filter size
)

// Resampler converts a device-rate mono stream to PipelineRate in chunks of any
// size, keeping filter state between calls so the result does not depend on
// how the stream was cut up.
//
// It is a polyphase implementation of rational resampling by L/M (for 48 kHz
// that is 1/3, for 44.1 kHz 160/441): conceptually zero-stuff by L, low-pass,
// keep every M-th sample, but the multiplications by stuffed zeros and the
// discarded samples are never computed. The low-pass is a linear-phase Kaiser
// windowed sinc with its cutoff halfway between the 7 kHz passband edge and
// the 8 kHz Nyquist of the output; Kaiser's beta is chosen for 70 dB of
// stopband so that anything the input carries above 8 kHz, which would
// otherwise fold into the singing band, is attenuated well past the 60 dB
// requirement. Being linear phase, the filter delays every frequency equally;
// GroupDelay reports that delay so callers can line the stream up with others.
//
// A Resampler is not safe for concurrent use.
type Resampler struct {
	inRate   int
	l, m     int
	taps     int       // taps per phase
	coef     []float64 // l phases of taps coefficients, reversed for a chronological window
	hist     []float64 // 2*taps mirror ring, see Process
	w        int       // ring write position
	d        int       // L*pushed - M*emitted: an output is due while d > 0
	passthru bool
	delay    float64
}

// NewResampler builds a converter from inRate Hz to PipelineRate. The input
// rate must be at least PipelineRate and reduce to a small integer ratio
// (44100 and 48000 both do).
func NewResampler(inRate int) (*Resampler, error) {
	if inRate < PipelineRate {
		return nil, fmt.Errorf("input rate %d Hz is below the %d Hz pipeline rate", inRate, PipelineRate)
	}
	g := gcd(inRate, PipelineRate)
	l, m := PipelineRate/g, inRate/g
	if l > resampleMaxPhases {
		return nil, fmt.Errorf("input rate %d Hz needs %d polyphase branches, more than the %d supported", inRate, l, resampleMaxPhases)
	}
	r := &Resampler{inRate: inRate, l: l, m: m}
	if l == 1 && m == 1 {
		r.passthru = true
		return r, nil
	}

	// Design at the interpolated rate l*inRate. Kaiser's length estimate for
	// the transition width and stopband depth above.
	hiRate := float64(l) * float64(inRate)
	width := (resampleStopEdgeHz - resamplePassEdgeHz) / hiRate
	length := int(math.Ceil((resampleStopDB - 7.95) / (14.36 * width)))
	r.taps = (length + l - 1) / l
	n := r.taps * l
	beta := 0.1102 * (resampleStopDB - 8.7)
	cutoff := (resamplePassEdgeHz + resampleStopEdgeHz) / 2 / hiRate // cycles per hi-rate sample
	centre := float64(n-1) / 2
	proto := make([]float64, n)
	var sum float64
	for i := range proto {
		x := float64(i) - centre
		s := 2 * cutoff
		if x != 0 {
			s = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		rr := x / (centre + 1e-12)
		w := besselI0(beta*math.Sqrt(math.Max(0, 1-rr*rr))) / besselI0(beta)
		proto[i] = s * w
		sum += proto[i]
	}
	// Interpolation by l spreads energy over l phases: DC gain must be l/sum
	// per full prototype so each phase averages to unity.
	scale := float64(l) / sum
	r.coef = make([]float64, n)
	for phi := 0; phi < l; phi++ {
		for t := 0; t < r.taps; t++ {
			j := r.taps - 1 - t
			r.coef[phi*r.taps+t] = proto[phi+j*l] * scale
		}
	}
	r.hist = make([]float64, 2*r.taps)
	r.delay = centre / float64(m)
	return r, nil
}

// InputRate is the device rate this converter was built for.
func (r *Resampler) InputRate() int { return r.inRate }

// GroupDelay is the delay the filter adds, in output samples (it need not be a
// whole number). A feature at input time t appears in the output at t plus this.
func (r *Resampler) GroupDelay() float64 { return r.delay }

// Reset forgets all stream state, as if the converter were new.
func (r *Resampler) Reset() {
	clear(r.hist)
	r.w, r.d = 0, 0
}

// Process appends the converted form of src to dst and returns the extended
// slice, in the style of append: pass dst[:0] of a reused buffer to avoid
// allocating. The number of samples produced depends on the stream position,
// about len(src)*16000/inRate.
func (r *Resampler) Process(dst, src []float32) []float32 {
	if r.passthru {
		return append(dst, src...)
	}
	for _, s := range src {
		// The ring is mirrored so the taps most recent samples are always one
		// contiguous, chronologically ordered slice hist[w : w+taps].
		x := float64(s)
		r.hist[r.w] = x
		r.hist[r.w+r.taps] = x
		r.w++
		if r.w == r.taps {
			r.w = 0
		}
		r.d += r.l
		for r.d > 0 {
			// M > L, so at most one output falls due per input sample and the
			// newest sample is always the first tap of the phase.
			phase := r.l - r.d
			win := r.hist[r.w : r.w+r.taps]
			cf := r.coef[phase*r.taps : (phase+1)*r.taps]
			var acc float64
			for t, c := range cf {
				acc += c * win[t]
			}
			dst = append(dst, float32(acc))
			r.d -= r.m
		}
	}
	return dst
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// besselI0 is the zeroth-order modified Bessel function of the first kind,
// by its power series, which converges quickly for the arguments a Kaiser
// window uses.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	q := x * x / 4
	for k := 1; k < 200; k++ {
		term *= q / float64(k*k)
		sum += term
		if term < sum*1e-16 {
			break
		}
	}
	return sum
}
