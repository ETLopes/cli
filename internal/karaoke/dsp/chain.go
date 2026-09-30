package dsp

import (
	"fmt"
)

// ChainConfig configures the cleaning chain of one microphone.
type ChainConfig struct {
	// Tail and BulkDelay configure the echo canceller (see CancellerConfig).
	Tail, BulkDelay int
	// ImpulseResponse, if not empty, is the calibration impulse response the
	// canceller is seeded from. Production always sets it; an unseeded chain
	// is the fallback for a missing or stale calibration.
	ImpulseResponse []float32
	// ResidualFloor, MarginDB and MaxAperiodicity configure the gate (see
	// GateConfig).
	ResidualFloor, MarginDB, MaxAperiodicity float64
}

// Frame is the pitch verdict for one 10 ms hop.
type Frame struct {
	// Index counts frames from zero. Frame k analyses the cleaned signal in
	// microphone samples [k*GateHop, k*GateHop+GateWindow), so its centre is
	// microphone sample Center().
	Index      int
	F0         float64 // Hz, 0 when not voiced
	Voiced     bool
	Confidence float64
	Level      float64 // RMS of the cleaned window, for diagnostics
}

// Center is the microphone sample index (at 16 kHz, counted from the first
// sample given to Process) at the middle of the frame's analysis window.
func (f Frame) Center() int { return f.Index*GateHop + GateWindow/2 }

const chainRing = 2048 // power of two, at least GateWindow plus one block

// Chain is the per-microphone signal path: echo canceller, residual echo
// suppressor, then a gate around YIN framing. It takes 16 kHz mono blocks of
// the playback reference and the microphone (resampling happens upstream) and
// emits a pitch Frame every GateHop samples.
//
// The suppressor delays the cleaned signal by SuppressorLatency samples; the
// chain delays the reference and the echo estimate by the same amount so all
// three stay aligned, and frame times are given on the microphone's own
// timeline. The first frame is available once GateWindow samples have been
// cleaned, so it appears GateWindow+SuppressorLatency samples into the
// stream.
//
// A Chain is not safe for concurrent use and allocates nothing per block
// after construction.
type Chain struct {
	canc *Canceller
	sup  *Suppressor
	gate *Gate

	o, e, res  []float32 // canceller output, echo estimate, suppressed output
	prevRef    []float32
	prevEcho   []float32
	outRing    [chainRing]float32
	refRing    [chainRing]float32
	echoRing   [chainRing]float32
	written    int // aligned samples in the rings
	next       int // index of the next frame
	wOut, wRef []float32
	wEcho      []float32
	started    bool
}

// NewChain builds a chain from cfg.
func NewChain(cfg ChainConfig) (*Chain, error) {
	canc, err := NewCanceller(CancellerConfig{Tail: cfg.Tail, BulkDelay: cfg.BulkDelay})
	if err != nil {
		return nil, fmt.Errorf("chain: %w", err)
	}
	if len(cfg.ImpulseResponse) > 0 {
		canc.SeedImpulseResponse(cfg.ImpulseResponse)
	}
	sup, err := NewSuppressor()
	if err != nil {
		return nil, fmt.Errorf("chain: %w", err)
	}
	gate, err := NewGate(GateConfig{ResidualFloor: cfg.ResidualFloor, MarginDB: cfg.MarginDB, MaxAperiodicity: cfg.MaxAperiodicity})
	if err != nil {
		return nil, fmt.Errorf("chain: %w", err)
	}
	b := CancellerBlock
	return &Chain{
		canc: canc, sup: sup, gate: gate,
		o: make([]float32, b), e: make([]float32, b), res: make([]float32, b),
		prevRef: make([]float32, b), prevEcho: make([]float32, b),
		wOut: make([]float32, GateWindow), wRef: make([]float32, GateWindow), wEcho: make([]float32, GateWindow),
	}, nil
}

// Process consumes one CancellerBlock-sample block of the reference and the
// microphone. Frames come every GateHop (160) samples, more than one block
// apart, so a block yields at most one; ok reports whether it did.
func (c *Chain) Process(ref, mic []float32) (f Frame, ok bool) {
	c.canc.Process(ref, mic, c.o, c.e)
	c.sup.Process(c.o, c.e, c.res)
	if c.started {
		// c.res is the block before this one; so are prevRef and prevEcho.
		for i := 0; i < CancellerBlock; i++ {
			j := (c.written + i) & (chainRing - 1)
			c.outRing[j], c.refRing[j], c.echoRing[j] = c.res[i], c.prevRef[i], c.prevEcho[i]
		}
		c.written += CancellerBlock
	}
	c.started = true
	copy(c.prevRef, ref)
	copy(c.prevEcho, c.e)

	start := c.next * GateHop
	if start+GateWindow > c.written {
		return Frame{}, false
	}
	for i := 0; i < GateWindow; i++ {
		j := (start + i) & (chainRing - 1)
		c.wOut[i], c.wRef[i], c.wEcho[i] = c.outRing[j], c.refRing[j], c.echoRing[j]
	}
	d := c.gate.Decide(c.wOut, c.wRef, c.wEcho)
	f = Frame{Index: c.next, F0: d.F0, Voiced: d.Voiced, Confidence: d.Confidence, Level: d.Level}
	c.next++
	return f, true
}
