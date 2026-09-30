package dsp

import (
	"math"
	"testing"
)

// suppressed is the chain canceller -> suppressor run over a scenario, with
// the suppressor's gains kept so any component of the mic (the echo, the
// singer) can be pushed through exactly the same time-varying filter. That is
// how the echo and the singer are measured separately even though the gain
// was computed from their sum.
type suppressed struct {
	canc  []float64   // canceller output
	est   []float64   // canceller echo estimate
	out   []float64   // suppressor output, aligned to the input (latency removed)
	gains [][]float64 // gains[f] was applied to the frame of blocks f-1 and f
}

func runSuppressed(t testing.TB, s *scenario, seeded bool) *suppressed {
	t.Helper()
	c := newTestCanceller(t)
	if seeded {
		s.seed(c)
	}
	sp, err := NewSuppressor()
	if err != nil {
		t.Fatal(err)
	}
	r := &suppressed{}
	r.canc, r.est = make([]float64, len(s.mic)), make([]float64, len(s.mic))
	raw := make([]float64, len(s.mic))
	ref, mic := f32(s.ref), f32(s.mic)
	o, e, y := make([]float32, cancBlock), make([]float32, cancBlock), make([]float32, cancBlock)
	for i := 0; i+cancBlock <= len(mic); i += cancBlock {
		c.Process(ref[i:i+cancBlock], mic[i:i+cancBlock], o, e)
		sp.Process(o, e, y)
		r.gains = append(r.gains, append([]float64(nil), sp.Gains()...))
		for j := range o {
			r.canc[i+j], r.est[i+j] = float64(o[j]), float64(e[j])
			raw[i+j] = float64(y[j])
		}
	}
	// y at call n is input block n-1.
	r.out = make([]float64, len(raw))
	copy(r.out, raw[SuppressorLatency:])
	return r
}

// through pushes x through the recorded gains, the way Suppressor.Process
// does, and returns the result aligned to x.
func (r *suppressed) through(t testing.TB, x []float64) []float64 {
	t.Helper()
	fft, err := NewRealFFT(supFFT)
	if err != nil {
		t.Fatal(err)
	}
	const b = CancellerBlock
	win := make([]float64, supFFT)
	for i := range win {
		win[i] = math.Sin(math.Pi * float64(i) / supFFT)
	}
	out := make([]float64, len(x))
	frame, spec := make([]float64, supFFT), make([]complex128, fft.Bins())
	for f, g := range r.gains {
		for i := range frame {
			if a := (f-1)*b + i; a >= 0 && a < len(x) {
				frame[i] = x[a] * win[i]
			} else {
				frame[i] = 0
			}
		}
		fft.Forward(frame, spec)
		for k := range spec {
			spec[k] *= complex(g[k], 0)
		}
		fft.Inverse(spec, frame)
		for i := range frame {
			if a := (f-1)*b + i; a >= 0 && a < len(out) {
				out[a] += frame[i] * win[i]
			}
		}
	}
	return out
}

func TestSuppressorReplayReproducesItsOwnOutput(t *testing.T) {
	cfg := mainScenario(3)
	cfg.singer, cfg.singerAt = 0.1, 1
	s := buildScenario(cfg)
	r := runSuppressed(t, s, true)
	replay := r.through(t, r.canc) // the suppressor's input is the canceller output
	var d, p float64
	for i := 2 * cancBlock; i < len(replay)-2*cancBlock; i++ {
		x := replay[i] - r.out[i]
		d += x * x
		p += r.out[i] * r.out[i]
	}
	if d > 1e-9*p {
		t.Errorf("replay differs from the real output by %.1f dB", 10*math.Log10(d/p))
	}
}

func TestSuppressorIsTransparentWithUnitGain(t *testing.T) {
	sp, err := NewSuppressor()
	if err != nil {
		t.Fatal(err)
	}
	x := f32(whiteNoise(cancBlock*40, 0.1, 3))
	echo := make([]float32, cancBlock) // no echo estimate: the gain stays 1
	res := make([]float32, len(x))
	for i := 0; i+cancBlock <= len(x); i += cancBlock {
		sp.Process(x[i:i+cancBlock], echo, res[i:i+cancBlock])
	}
	for i := cancBlock; i < len(x)-cancBlock; i++ {
		if d := math.Abs(float64(res[i+cancBlock] - x[i])); d > 1e-5 {
			t.Fatalf("sample %d differs by %g", i, d)
		}
	}
}

func TestSuppressorRemovesAFurther8DBOfEchoAfterTheSeededCanceller(t *testing.T) {
	s := buildScenario(mainScenario(8))
	r := runSuppressed(t, s, true)
	canc := s.erle(r.canc, 2, 8)
	total := s.erle(r.out, 2, 8)
	t.Logf("canceller ERLE %.1f dB, with suppressor %.1f dB (further %.1f dB)", canc, total, total-canc)
	if total-canc < 8 {
		t.Errorf("suppressor removed a further %.1f dB, want at least 8", total-canc)
	}
	if total < 40 {
		t.Errorf("total echo reduction %.1f dB, want at least 40", total)
	}
}

func TestSuppressorKeepsTheSingerDuringDoubleTalkAfterSeeding(t *testing.T) {
	cfg := mainScenario(12)
	cfg.singer, cfg.singerAt = 0.1, 6
	s := buildScenario(cfg)
	r := runSuppressed(t, s, true)
	// The singer through the gains alone: what the suppressor did to the voice.
	sung := r.through(t, s.singer)
	var os, ss float64
	for i := int(6.5 * cancRate); i < 12*cancRate; i++ {
		os += sung[i] * s.singer[i]
		ss += s.singer[i] * s.singer[i]
	}
	gain := 20 * math.Log10(os/ss)
	total := s.singerGainDB(r.out, 6.5, 12)
	hit := s.pitchHitRate(t, r.out, 6.5, 12)
	hitCanc := s.pitchHitRate(t, r.canc, 6.5, 12)
	t.Logf("canceller alone: ERLE %.1f dB, singer gain %.2f dB", s.erle(r.canc, 6.5, 12), s.singerGainDB(r.canc, 6.5, 12))
	// The echo left by the canceller, separated from the singer it passes
	// through untouched, run through the same gains.
	left := make([]float64, len(r.canc))
	for i := range left {
		left[i] = r.canc[i] - s.singer[i]
	}
	echoERLE := erleDB(s.echo, r.through(t, left), int(6.5*cancRate), 12*cancRate)
	t.Logf("singer gain through the gains alone %.2f dB, in the output %.2f dB; pitch hits %.1f%% (canceller alone %.1f%%); separable echo reduction during singing %.1f dB",
		gain, total, 100*hit, 100*hitCanc, echoERLE)
	if math.Abs(gain) > 1.5 || math.Abs(total) > 1.5 {
		t.Errorf("singer level changed by %.2f dB (%.2f dB in the output), want within 1.5", gain, total)
	}
	if hit < 0.9 {
		t.Errorf("pitch tracked in %.1f%% of voiced frames after suppression, want at least 90%%", 100*hit)
	}
}

func TestSuppressorDoesNotAllocateInSteadyState(t *testing.T) {
	sp, err := NewSuppressor()
	if err != nil {
		t.Fatal(err)
	}
	out, echo, res := make([]float32, cancBlock), make([]float32, cancBlock), make([]float32, cancBlock)
	for i := range out {
		out[i], echo[i] = float32(math.Sin(float64(i)*0.3)), float32(math.Sin(float64(i)*0.3+1))
	}
	for i := 0; i < 50; i++ {
		sp.Process(out, echo, res)
	}
	if n := testing.AllocsPerRun(50, func() { sp.Process(out, echo, res) }); n != 0 {
		t.Errorf("Process allocates %.0f times per block, want 0", n)
	}
}
