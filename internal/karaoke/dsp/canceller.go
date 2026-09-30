package dsp

import (
	"fmt"
	"math"
)

// CancellerBlock is the number of samples the Canceller consumes per call:
// 8 ms at 16 kHz. It is the partition size of the filter and its algorithmic
// latency.
const CancellerBlock = 128

// CancellerConfig configures an echo canceller for one microphone.
type CancellerConfig struct {
	// Tail is the length of the echo path the filter models, in samples at
	// 16 kHz, counted after BulkDelay and rounded up to whole blocks.
	Tail int
	// BulkDelay is the known delay between a reference sample being played and
	// its first arrival at the microphone. The reference is delayed by this
	// much so the partitions span only the real tail.
	BulkDelay int
}

// Tuning constants; see the Canceller documentation for what each governs.
const (
	cancSmooth      = 0.95 // per-block smoothing of the path error powers (about 160 ms)
	cancCopyRatio   = 0.5  // background/foreground error ratio that triggers a copy ...
	cancCopyBlocks  = 4    // ... when seen this many blocks in a row
	cancSlowRatio   = 0.85 // a smaller advantage must persist longer, otherwise a
	cancSlowBlocks  = 24   // converged foreground never picks up the last few dB
	cancResetRatio  = 2.0  // background this much worse than foreground is reset
	cancResetBlocks = 4    // ... when seen this many blocks in a row
	cancErrSmooth   = 0.5  // per-block smoothing of the per-bin error power
	cancPsiScale    = 0.3
	cancP0          = 1.0   // initial weight uncertainty
	cancQ           = 2e-2  // per-block process noise, as a fraction of the weight power
	cancQFloor      = 1e-6  // process noise floor so an empty partition can wake up
	cancSeedP       = 1e-3  // weight uncertainty of a seeded filter, as a fraction of the weight power
	cancRegAbs      = 1e-10 // floor of the innovation variance
	cancDivergeBlk  = 60    // blocks of output louder than 2x mic before a reset
)

// path is one adaptive filter: P partitions of frequency-domain weights plus
// the time-domain echo estimate it produced for the current block.
type path struct {
	w [][]complex128
	y [CancellerBlock]float64
}

// Canceller is a partitioned-block frequency-domain adaptive filter (PBFDAF,
// overlap-save, FFT size 2*CancellerBlock, gradient constraint on every
// partition every block) that subtracts the loudspeaker echo of a known
// reference from one microphone.
//
// # Two paths
//
// An adaptive background filter is updated every block; a foreground filter
// produces the output. When the background's smoothed error is consistently
// lower than the foreground's it is copied over the foreground; when it is
// clearly worse (it absorbed a singer and diverged) it is reset to the
// foreground. A singer entering can therefore at worst stall improvement: the
// foreground keeps its converged cancellation.
//
// # Step control
//
// The background weights are updated with a per-partition, per-bin step equal
// to a diagonal frequency-domain Kalman gain (Enzner and Vary, 2006): every
// weight carries an uncertainty P, the step is P*conj(X)/S with
// S = sum(P*|X|^2) + psi, and psi, the observation noise, is the smoothed error
// power of the bin. P shrinks as a weight is confirmed and regrows with process
// noise proportional to the weight's own power. This is the same idea as
// Valin's residual-to-error ratio (2007, SpeexDSP mdf.c), a step that is large
// while the error is echo and small while it is something else, but with the
// residual echo taken from the weights' uncertainty instead of from the
// correlation between error and echo estimate.
//
// Why not Valin's leakage eta = Pey/Pyy: measured on the synthetic room, eta
// was about 0 while the filter was still 10 dB from converged. The residual
// echo then lies in partitions that are still empty, and their contribution is
// uncorrelated with the echo estimate built from the partitions that are not,
// so eta reads "nothing left to learn" and the step collapses. The uncertainty
// has no such blind spot: an unlearned partition has a large P whatever the
// estimate looks like. A singer raises psi, which lowers every step at once,
// and the two-path arbitration catches whatever leaks through.
//
// A Canceller is not safe for concurrent use.
type Canceller struct {
	p, bins int
	fft     *RealFFT
	delay   int

	ring     []float64
	ringMask int
	pos      int

	xf   [][]complex128 // xf[(head+i)%p] is the reference frame i blocks old
	head int
	fg   path
	bg   path
	unc  [][]float64 // weight uncertainty of the background filter, per partition and bin

	frame    []float64
	acc, ef  []complex128
	see      []float64 // smoothed |E|^2 per bin: the observation noise
	innov    []float64 // innovation variance per bin
	pFg, pBg float64   // smoothed error powers of the two paths
	pMic     float64
	better   int
	slow     int
	worse    int
	louder   int
}

// NewCanceller builds a canceller with zero weights.
func NewCanceller(cfg CancellerConfig) (*Canceller, error) {
	if cfg.Tail < 1 {
		return nil, fmt.Errorf("canceller tail %d must be positive", cfg.Tail)
	}
	if cfg.BulkDelay < 0 {
		return nil, fmt.Errorf("canceller bulk delay %d must not be negative", cfg.BulkDelay)
	}
	fft, err := NewRealFFT(2 * CancellerBlock)
	if err != nil {
		return nil, fmt.Errorf("canceller: %w", err)
	}
	c := &Canceller{fft: fft, bins: fft.Bins(), delay: cfg.BulkDelay}
	c.p = (cfg.Tail + CancellerBlock - 1) / CancellerBlock
	size := 1
	for size < cfg.BulkDelay+3*CancellerBlock {
		size <<= 1
	}
	c.ring, c.ringMask = make([]float64, size), size-1
	c.xf = make([][]complex128, c.p)
	c.fg.w, c.bg.w = make([][]complex128, c.p), make([][]complex128, c.p)
	c.unc = make([][]float64, c.p)
	for i := range c.xf {
		c.xf[i] = make([]complex128, c.bins)
		c.fg.w[i] = make([]complex128, c.bins)
		c.bg.w[i] = make([]complex128, c.bins)
		c.unc[i] = make([]float64, c.bins)
	}
	c.frame = make([]float64, fft.Size())
	c.acc, c.ef = make([]complex128, c.bins), make([]complex128, c.bins)
	c.see, c.innov = make([]float64, c.bins), make([]float64, c.bins)
	c.Reset()
	return c, nil
}

// Block is the number of samples Process consumes per call.
func (c *Canceller) Block() int { return CancellerBlock }

// Partitions is the number of filter partitions.
func (c *Canceller) Partitions() int { return c.p }

// Reset forgets everything learned, including any seed.
func (c *Canceller) Reset() {
	clear(c.ring)
	c.pos, c.head = 0, 0
	for i := range c.xf {
		clear(c.xf[i])
		clear(c.fg.w[i])
		clear(c.bg.w[i])
		for k := range c.unc[i] {
			c.unc[i][k] = cancP0
		}
	}
	clear(c.see)
	c.pFg, c.pBg, c.pMic = 0, 0, 0
	c.better, c.slow, c.worse, c.louder = 0, 0, 0, 0
}

// SeedImpulseResponse loads both filters from a measured impulse response
// (see Sweep.ImpulseResponse), indexed from the moment the reference was
// played: the first BulkDelay samples are skipped. A seeded filter starts with
// a small weight uncertainty, so it is refined rather than overwritten.
func (c *Canceller) SeedImpulseResponse(ir []float32) {
	c.Reset()
	if c.delay >= len(ir) {
		return
	}
	ir = ir[c.delay:]
	for p := 0; p < c.p; p++ {
		clear(c.frame)
		for i := 0; i < CancellerBlock; i++ {
			if j := p*CancellerBlock + i; j < len(ir) {
				c.frame[i] = float64(ir[j])
			}
		}
		c.fft.Forward(c.frame, c.fg.w[p])
		copy(c.bg.w[p], c.fg.w[p])
		for k, w := range c.fg.w[p] {
			c.unc[p][k] = cancSeedP*(real(w)*real(w)+imag(w)*imag(w)) + cancQFloor
		}
	}
}

// Process cancels one block. ref is the playback block, mic the microphone
// block captured at the same time; both, and out, must be CancellerBlock long.
// out receives the foreground error (the echo-cancelled signal) and echo, if
// not nil, the foreground echo estimate. out may alias mic.
func (c *Canceller) Process(ref, mic, out, echo []float32) {
	const b = CancellerBlock
	if len(ref) != b || len(mic) != b || len(out) != b || (echo != nil && len(echo) != b) {
		panic(fmt.Sprintf("dsp: canceller works in blocks of %d samples", b))
	}
	for i, v := range ref {
		c.ring[(c.pos+i)&c.ringMask] = float64(v)
	}
	for i := range c.frame {
		if a := c.pos - b - c.delay + i; a >= 0 {
			c.frame[i] = c.ring[a&c.ringMask]
		} else {
			c.frame[i] = 0
		}
	}
	c.head = (c.head - 1 + c.p) % c.p
	c.fft.Forward(c.frame, c.xf[c.head])

	c.estimate(&c.fg)
	c.estimate(&c.bg)

	var micPow, fgPow, bgPow float64
	for i := 0; i < b; i++ {
		m := float64(mic[i])
		ef, eb := m-c.fg.y[i], m-c.bg.y[i]
		micPow += m * m
		fgPow += ef * ef
		bgPow += eb * eb
	}
	if math.IsNaN(fgPow+bgPow) || math.IsInf(fgPow+bgPow, 0) {
		c.Reset()
		copy(out, mic)
		if echo != nil {
			clear(echo)
		}
		return
	}
	for i := 0; i < b; i++ {
		out[i] = float32(float64(mic[i]) - c.fg.y[i])
		if echo != nil {
			echo[i] = float32(c.fg.y[i])
		}
	}

	c.arbitrate(micPow, fgPow, bgPow)
	c.adapt(mic)
	c.pos += b
}

// estimate forms the path's echo estimate for the current block: the sum over
// partitions of weights times the reference frame that many blocks old, then
// the last half of the inverse transform (overlap-save).
func (c *Canceller) estimate(p *path) {
	const b = CancellerBlock
	clear(c.acc)
	for i := 0; i < c.p; i++ {
		x, w := c.xf[(c.head+i)%c.p], p.w[i]
		for k, xv := range x {
			c.acc[k] += w[k] * xv
		}
	}
	c.fft.Inverse(c.acc, c.frame)
	copy(p.y[:], c.frame[b:])
}

// arbitrate tracks the error power of both paths and moves weights between
// them: background over foreground when it is consistently better, foreground
// over background when it is clearly worse. A foreground that makes the output
// persistently louder than the microphone has diverged and resets everything.
func (c *Canceller) arbitrate(micPow, fgPow, bgPow float64) {
	const floor = 1e-12
	c.pMic = cancSmooth*c.pMic + (1-cancSmooth)*micPow
	c.pFg = cancSmooth*c.pFg + (1-cancSmooth)*fgPow
	c.pBg = cancSmooth*c.pBg + (1-cancSmooth)*bgPow

	if fgPow > 2*micPow+1e-9 {
		c.louder++
	} else {
		c.louder = 0
	}
	if c.louder > cancDivergeBlk {
		c.Reset()
		return
	}

	ratio := (c.pBg + floor) / (c.pFg + floor)
	c.better = countIf(ratio < cancCopyRatio, c.better)
	c.slow = countIf(ratio < cancSlowRatio, c.slow)
	c.worse = countIf(ratio > cancResetRatio, c.worse)
	switch {
	case c.better >= cancCopyBlocks || c.slow >= cancSlowBlocks:
		for i := range c.fg.w {
			copy(c.fg.w[i], c.bg.w[i])
		}
		c.pFg = c.pBg
		c.better, c.slow = 0, 0
	case c.worse >= cancResetBlocks:
		for i := range c.bg.w {
			copy(c.bg.w[i], c.fg.w[i])
		}
		c.pBg = c.pFg
		c.worse = 0
	}
}

// adapt updates the background weights and their uncertainties from the
// background error of this block.
func (c *Canceller) adapt(mic []float32) {
	const b = CancellerBlock
	// Spectrum of the background error, zero-padded at the front so it shares
	// the reference frame's overlap-save alignment.
	clear(c.frame[:b])
	for i := 0; i < b; i++ {
		c.frame[b+i] = float64(mic[i]) - c.bg.y[i]
	}
	c.fft.Forward(c.frame, c.ef)

	// Innovation variance: what the weights' uncertainty predicts for the
	// error, plus the observation noise.
	for k := range c.innov {
		e := c.ef[k]
		c.see[k] = cancErrSmooth*c.see[k] + (1-cancErrSmooth)*(real(e)*real(e)+imag(e)*imag(e))
		c.innov[k] = cancPsiScale*c.see[k] + cancRegAbs
	}
	for i := 0; i < c.p; i++ {
		x, u := c.xf[(c.head+i)%c.p], c.unc[i]
		for k, xv := range x {
			c.innov[k] += u[k] * (real(xv)*real(xv) + imag(xv)*imag(xv))
		}
	}

	for i := 0; i < c.p; i++ {
		x, w, u := c.xf[(c.head+i)%c.p], c.bg.w[i], c.unc[i]
		for k, xv := range x {
			g := u[k] / c.innov[k]
			w[k] += complex(real(xv), -imag(xv)) * c.ef[k] * complex(g, 0)
		}
		// Gradient constraint: only the first block of taps may be non-zero.
		c.fft.Inverse(w, c.frame)
		clear(c.frame[b:])
		c.fft.Forward(c.frame, w)
		for k, xv := range x {
			p := u[k]
			p -= p * p * (real(xv)*real(xv) + imag(xv)*imag(xv)) / c.innov[k]
			u[k] = p + cancQ*(real(w[k])*real(w[k])+imag(w[k])*imag(w[k])) + cancQFloor
		}
	}
}

func countIf(cond bool, n int) int {
	if cond {
		return n + 1
	}
	return 0
}
