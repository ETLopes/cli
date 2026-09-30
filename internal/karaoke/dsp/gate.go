package dsp

import (
	"fmt"
	"math"
)

// GateWindow is the number of samples of each signal a Gate looks at: the YIN
// window, 64 ms at 16 kHz.
const GateWindow = 1024

// GateHop is the spacing of pitch frames, 10 ms at 16 kHz.
const GateHop = 160

// Tuning constants of the gate.
const (
	gateDefaultMarginDB   = 6.0
	gateDefaultMaxAperiod = 0.15
	gateCentsTolerance    = 50.0 // how far from f0 a reference partial may sit and still explain it
	gateStrongPartialDB   = -30  // a reference partial this close to the strongest in range counts as strong
	gateEchoShare         = 0.01 // output power below this share of the echo estimate's power is echo residual
	gateSearchLow         = 60.0 // Hz, range of the reference spectrum the partial is compared against
	gateSearchHigh        = 1100.0
)

// GateConfig configures a Gate.
type GateConfig struct {
	// ResidualFloor is the RMS of the cleaned output with backing only,
	// measured at calibration. A frame is only trusted when it is louder than
	// this by MarginDB.
	ResidualFloor float64
	// MarginDB is the margin above ResidualFloor; 6 dB when zero.
	MarginDB float64
	// MaxAperiodicity is the largest YIN aperiodicity of a trusted frame;
	// 0.15 when zero.
	MaxAperiodicity float64
}

// Decision is the verdict on one frame.
type Decision struct {
	F0         float64 // Hz, 0 when the frame is not trusted singing
	Voiced     bool    // true when F0 is trustworthy singing
	Confidence float64 // 1 - aperiodicity, in [0, 1]; 0 when not voiced
	Level      float64 // RMS of the cleaned output over the window
}

// Gate decides, frame by frame, whether the pitch YIN finds in the cleaned
// microphone signal is a person singing rather than what is left of the
// backing track. A frame is accepted only when all three hold:
//
//  1. Its RMS is above the calibrated residual floor plus a margin. Backing
//     residue is at the floor by definition.
//  2. YIN's aperiodicity is below a threshold: a clear period.
//  3. The reference does not explain the pitch. If the reference window has
//     a strong partial at f0, f0/2 or 2*f0 (within 50 cents) and the cleaned
//     output is weak next to the echo estimate, what YIN heard is the
//     backing's own note leaking through the canceller. A singer on the same
//     note is louder than the residue, so the second condition lets them
//     through.
//
// A Gate is not safe for concurrent use and allocates nothing per frame.
type Gate struct {
	cfg       GateConfig
	minLevel  float64
	yin       *YIN
	fft       *RealFFT
	win       []float64
	buf       []float64
	spec      []complex128
	pow       []float64
	binHz     float64
	loBin     int
	hiBin     int
	strongMin float64
}

// NewGate validates cfg and prepares a gate.
func NewGate(cfg GateConfig) (*Gate, error) {
	if cfg.MarginDB == 0 {
		cfg.MarginDB = gateDefaultMarginDB
	}
	if cfg.MaxAperiodicity == 0 {
		cfg.MaxAperiodicity = gateDefaultMaxAperiod
	}
	if cfg.ResidualFloor < 0 {
		return nil, fmt.Errorf("gate residual floor %v must not be negative", cfg.ResidualFloor)
	}
	if cfg.MaxAperiodicity <= 0 || cfg.MaxAperiodicity >= 1 {
		return nil, fmt.Errorf("gate aperiodicity limit %v must be between 0 and 1", cfg.MaxAperiodicity)
	}
	yin, err := NewYIN(YINConfig{SampleRate: PipelineRate, Window: GateWindow, FMin: 60, FMax: 1100})
	if err != nil {
		return nil, fmt.Errorf("gate: %w", err)
	}
	fft, err := NewRealFFT(GateWindow)
	if err != nil {
		return nil, fmt.Errorf("gate: %w", err)
	}
	g := &Gate{cfg: cfg, yin: yin, fft: fft}
	g.minLevel = cfg.ResidualFloor * math.Pow(10, cfg.MarginDB/20)
	g.win = make([]float64, GateWindow)
	for i := range g.win {
		g.win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/GateWindow)
	}
	g.buf, g.spec = make([]float64, GateWindow), make([]complex128, fft.Bins())
	g.pow = make([]float64, fft.Bins())
	g.binHz = float64(PipelineRate) / GateWindow
	g.loBin, g.hiBin = int(math.Ceil(gateSearchLow/g.binHz)), int(gateSearchHigh/g.binHz)
	g.strongMin = math.Pow(10, gateStrongPartialDB/10)
	return g, nil
}

// Decide judges one frame. out is the cleaned microphone signal, ref the
// playback reference and echo the canceller's echo estimate over the same
// GateWindow samples.
func (g *Gate) Decide(out, ref, echo []float32) Decision {
	if len(out) != GateWindow || len(ref) != GateWindow || len(echo) != GateWindow {
		panic(fmt.Sprintf("dsp: gate works on windows of %d samples", GateWindow))
	}
	var outPow, echoPow float64
	for i, v := range out {
		outPow += float64(v) * float64(v)
		echoPow += float64(echo[i]) * float64(echo[i])
	}
	d := Decision{Level: math.Sqrt(outPow / GateWindow)}
	if d.Level <= g.minLevel || d.Level == 0 {
		return d
	}
	f0, ap := g.yin.Detect(out)
	if f0 <= 0 || ap > g.cfg.MaxAperiodicity {
		return d
	}
	if outPow < gateEchoShare*echoPow && g.referenceHasPartial(ref, f0) {
		return d
	}
	d.F0, d.Voiced, d.Confidence = f0, true, math.Min(1, math.Max(0, 1-ap))
	return d
}

// referenceHasPartial reports whether the reference has a strong spectral
// peak (a local maximum of the spectrum, so the skirt of a neighbouring note
// does not count) at f0 or an octave of it.
func (g *Gate) referenceHasPartial(ref []float32, f0 float64) bool {
	for i, v := range ref {
		g.buf[i] = float64(v) * g.win[i]
	}
	g.fft.Forward(g.buf, g.spec)
	var top float64
	for k := g.loBin; k <= g.hiBin; k++ {
		p := real(g.spec[k])*real(g.spec[k]) + imag(g.spec[k])*imag(g.spec[k])
		g.pow[k] = p
		top = math.Max(top, p)
	}
	if top <= 0 {
		return false
	}
	span := math.Pow(2, gateCentsTolerance/1200)
	for k := max(g.loBin, 1); k <= g.hiBin; k++ {
		a, b, c := g.pow[k-1], g.pow[k], g.pow[k+1]
		if b < g.strongMin*top || b < a || b < c || a <= 0 || c <= 0 {
			continue
		}
		// Parabola through the log powers of the peak and its neighbours.
		la, lb, lc := math.Log(a), math.Log(b), math.Log(c)
		peak := (float64(k) + 0.5*(la-lc)/(la-2*lb+lc)) * g.binHz
		for _, f := range [...]float64{f0, 2 * f0, f0 / 2} {
			if peak > f/span && peak < f*span {
				return true
			}
		}
	}
	return false
}
