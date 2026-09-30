package dsp

import (
	"fmt"
	"math"
)

// SuppressorLatency is the delay the Suppressor adds, in samples: one
// CancellerBlock (8 ms at 16 kHz). Its overlap-add synthesis cannot finish a
// block until the next one has been analysed.
const SuppressorLatency = CancellerBlock

// Tuning constants of the residual echo suppressor.
const (
	supFFT       = 2 * CancellerBlock
	supSmooth    = 0.9   // per-block smoothing of the echo and cross powers (about 80 ms)
	supLeakMin   = 1e-3  // the residual is never assumed below this share of the echo estimate
	supLeakMax   = 1.0   // ... nor above this one
	supOver      = 1.5   // over-subtraction of the residual estimate
	supFloor     = 0.1   // gain floor, -20 dB: a deeper cut leaves musical noise for YIN to chase
	supRise      = 0.4   // gain smoothing when the gain goes up (fast, so a singer is not clipped)
	supFall      = 0.75  // gain smoothing when the gain goes down (slow, so the gain does not flutter)
	supYSmooth   = 0.5   // smoothing of the output power the gain is measured against
	supEpsilon   = 1e-12 // keeps ratios finite in digital silence
	supPowerHold = 1e-9  // echo power below which a bin has no echo to remove
)

// Suppressor removes the echo the Canceller leaves behind, with a per-bin
// Wiener-style gain on the canceller output.
//
// # Algorithm
//
// Blocks of CancellerBlock samples are analysed in 2*CancellerBlock frames
// with 50 % overlap and sqrt-Hann windows (which sum to one when squared and
// overlap-added), so an unchanged gain gives back the input exactly.
//
// The residual echo in a bin is modelled as leak * |E|^2 where E is the echo
// estimate the canceller subtracted. The leak factor is the squared
// regression coefficient of the output Y on E: Re(Pye)/Pee, with Pye the
// smoothed cross-power of Y and E and Pee the smoothed power of E. A residual
// that is a scaled copy of the estimate makes the coefficient equal that
// scale; a singer, who is uncorrelated with the estimate, averages to zero,
// which is what keeps the gain near 0 dB when the output is voice. The factor
// is clamped to [supLeakMin, supLeakMax]: the lower clamp assumes some
// residual even where none is correlated (the nonlinear part of a speaker's
// echo is not, and is what remains once the linear part is gone).
//
// The gain is 1 - supOver*residual/|Y|^2, floored at -20 dB and smoothed in
// time, quickly upward and slowly downward.
//
// Latency is SuppressorLatency samples: the block returned by Process is the
// one passed to the previous call, and the first call returns silence.
//
// A Suppressor is not safe for concurrent use and allocates nothing per block.
type Suppressor struct {
	fft  *RealFFT
	bins int
	win  []float64

	prevY, prevE []float64 // last block of the output and the echo estimate
	tail         []float64 // second half of the previous synthesis frame
	fy, fe       []float64
	sy, se       []complex128
	pee          []float64
	pye          []float64 // real part of the cross-power is all the regression needs
	ys           []float64
	gain         []float64
}

// NewSuppressor builds a suppressor for CancellerBlock-sample blocks.
func NewSuppressor() (*Suppressor, error) {
	fft, err := NewRealFFT(supFFT)
	if err != nil {
		return nil, fmt.Errorf("suppressor: %w", err)
	}
	s := &Suppressor{fft: fft, bins: fft.Bins()}
	s.win = make([]float64, supFFT)
	for i := range s.win {
		s.win[i] = math.Sin(math.Pi * float64(i) / supFFT)
	}
	b := CancellerBlock
	s.prevY, s.prevE, s.tail = make([]float64, b), make([]float64, b), make([]float64, b)
	s.fy, s.fe = make([]float64, supFFT), make([]float64, supFFT)
	s.sy, s.se = make([]complex128, s.bins), make([]complex128, s.bins)
	s.pee, s.pye, s.ys, s.gain = make([]float64, s.bins), make([]float64, s.bins), make([]float64, s.bins), make([]float64, s.bins)
	s.Reset()
	return s, nil
}

// Reset forgets all state.
func (s *Suppressor) Reset() {
	clear(s.prevY)
	clear(s.prevE)
	clear(s.tail)
	clear(s.pee)
	clear(s.pye)
	clear(s.ys)
	for k := range s.gain {
		s.gain[k] = 1
	}
}

// Gains returns the per-bin gain applied by the last Process call. The slice
// is owned by the Suppressor and only valid until the next call.
func (s *Suppressor) Gains() []float64 { return s.gain }

// Process suppresses the residual echo in one block. out is the canceller's
// output and echo its echo estimate for the same block, both CancellerBlock
// long; res receives the suppressed signal delayed by SuppressorLatency and
// may alias out.
func (s *Suppressor) Process(out, echo, res []float32) {
	const b = CancellerBlock
	if len(out) != b || len(echo) != b || len(res) != b {
		panic(fmt.Sprintf("dsp: suppressor works in blocks of %d samples", b))
	}
	for i := 0; i < b; i++ {
		s.fy[i], s.fe[i] = s.prevY[i], s.prevE[i]
		s.fy[b+i], s.fe[b+i] = float64(out[i]), float64(echo[i])
	}
	for i := 0; i < b; i++ {
		s.prevY[i], s.prevE[i] = float64(out[i]), float64(echo[i])
	}
	for i, w := range s.win {
		s.fy[i] *= w
		s.fe[i] *= w
	}
	s.fft.Forward(s.fy, s.sy)
	s.fft.Forward(s.fe, s.se)

	for k := 0; k < s.bins; k++ {
		y, e := s.sy[k], s.se[k]
		y2 := real(y)*real(y) + imag(y)*imag(y)
		e2 := real(e)*real(e) + imag(e)*imag(e)
		s.pee[k] = supSmooth*s.pee[k] + (1-supSmooth)*e2
		s.pye[k] = supSmooth*s.pye[k] + (1-supSmooth)*(real(y)*real(e)+imag(y)*imag(e))
		s.ys[k] = supYSmooth*s.ys[k] + (1-supYSmooth)*y2

		g := 1.0
		if s.pee[k] > supPowerHold {
			eta := s.pye[k] / s.pee[k]
			leak := math.Min(supLeakMax, math.Max(supLeakMin, eta*eta))
			g = 1 - supOver*leak*e2/(s.ys[k]+supEpsilon)
			g = math.Max(supFloor, g)
		}
		if g > s.gain[k] {
			g = supRise*s.gain[k] + (1-supRise)*g
		} else {
			g = supFall*s.gain[k] + (1-supFall)*g
		}
		s.gain[k] = g
		s.sy[k] = y * complex(g, 0)
	}

	s.fft.Inverse(s.sy, s.fy)
	for i := 0; i < b; i++ {
		res[i] = float32(s.tail[i] + s.fy[i]*s.win[i])
		s.tail[i] = s.fy[b+i] * s.win[b+i]
	}
}
