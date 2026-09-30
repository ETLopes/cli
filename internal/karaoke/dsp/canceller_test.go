package dsp

import (
	"testing"
)

const (
	cancTail  = 4800 // 300 ms
	cancBulk  = 320  // 20 ms
	cancRate  = testRate
	cancBlock = 128
)

// scenario is one separable synthetic recording: the echo, the noise floor and
// the singer are kept apart so ERLE can be measured exactly, whatever the
// canceller does with the mix.
type scenario struct {
	ref, echo, noise, singer, truth, mic []float64
}

type scenarioConfig struct {
	seconds  float64
	rt60     float64
	drive    float64 // soft-clip drive; 0 disables
	noise    float64 // noise floor sigma
	singer   float64 // singer level; 0 for none
	singerAt float64 // seconds before the singer starts
	seed     int64
}

// mainScenario is a mildly nonlinear speaker and a low noise floor: the room
// in which the adaptive targets are asserted. harshScenario keeps a strong
// nonlinearity and a higher noise floor and is only logged.
func mainScenario(seconds float64) scenarioConfig {
	return scenarioConfig{seconds: seconds, rt60: 0.3, drive: 1.0, noise: 1e-4, seed: 1}
}

func harshScenario(seconds float64) scenarioConfig {
	return scenarioConfig{seconds: seconds, rt60: 0.3, drive: 2.5, noise: 3e-4, seed: 1}
}

func buildScenario(c scenarioConfig) *scenario {
	n := int(c.seconds * cancRate)
	s := &scenario{ref: musicReference(n, c.seed)}
	r := newRoom(roomConfig{bulkDelay: cancBulk, rt60: c.rt60, seed: c.seed + 10})
	s.echo = r.apply(softClip(s.ref, c.drive))
	s.noise = whiteNoise(n, c.noise, c.seed+20)
	s.singer = make([]float64, n)
	s.truth = make([]float64, n)
	if c.singer > 0 {
		off := int(c.singerAt * cancRate)
		sg, tr := singerLine(n-off, c.singer, c.seed+30)
		copy(s.singer[off:], sg)
		copy(s.truth[off:], tr)
	}
	s.mic = sumSignals(s.echo, s.noise, s.singer)
	return s
}

// residual is what the canceller left of the echo, noise floor included: the
// output minus the singer, which a linear canceller passes through.
func (s *scenario) residual(out []float64) []float64 {
	r := make([]float64, len(out))
	for i := range r {
		r[i] = out[i] - s.singer[i]
	}
	return r
}

func (s *scenario) erle(out []float64, fromSec, toSec float64) float64 {
	return erleDB(s.echo, s.residual(out), int(fromSec*cancRate), int(toSec*cancRate))
}

func negate(x []float64) []float64 {
	o := make([]float64, len(x))
	for i, v := range x {
		o[i] = -v
	}
	return o
}

func TestOracleFilterCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  scenarioConfig
		min  float64
	}{
		{"main", mainScenario(10), 30},
		{"harsh", harshScenario(10), 0},
	} {
		s := buildScenario(tc.cfg)
		r := newRoom(roomConfig{bulkDelay: cancBulk, rt60: tc.cfg.rt60, seed: tc.cfg.seed + 10})
		ir := r.ir[:min(len(r.ir), cancBulk+((cancTail+cancBlock-1)/cancBlock)*cancBlock)]
		y := fftConvolve(s.ref, ir)[:len(s.ref)]
		out := sumSignals(s.mic, negate(y))
		got := s.erle(out, 2, 10)
		t.Logf("%s: oracle ceiling %.1f dB", tc.name, got)
		if got < tc.min {
			t.Errorf("%s oracle ceiling %.1f dB, want at least %.0f", tc.name, got, tc.min)
		}
	}
}

// runCanceller streams ref and mic through c block by block and returns the
// output and the echo estimate. Trailing samples that do not fill a block stay
// zero.
func runCanceller(t testing.TB, c *Canceller, ref, mic []float64, probe ...func(sample int)) (out, est []float64) {
	t.Helper()
	out, est = make([]float64, len(mic)), make([]float64, len(mic))
	r32, m32 := f32(ref), f32(mic)
	o, e := make([]float32, cancBlock), make([]float32, cancBlock)
	for i := 0; i+cancBlock <= len(mic); i += cancBlock {
		c.Process(r32[i:i+cancBlock], m32[i:i+cancBlock], o, e)
		for j := range o {
			out[i+j], est[i+j] = float64(o[j]), float64(e[j])
		}
		for _, p := range probe {
			p(i)
		}
	}
	return out, est
}

func newTestCanceller(t testing.TB) *Canceller {
	t.Helper()
	c, err := NewCanceller(CancellerConfig{Tail: cancTail, BulkDelay: cancBulk})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCancellerConvergesOnMusicWithNoSinger(t *testing.T) {
	s := buildScenario(mainScenario(10))
	c := newTestCanceller(t)
	out, _ := runCanceller(t, c, s.ref, s.mic, func(i int) {
		if i%cancRate < cancBlock {
			t.Logf("  t=%ds pMic %.2e pFg %.2e pBg %.2e", i/cancRate, c.pMic, c.pFg, c.pBg)
		}
	})
	early, late := s.erle(out, 3, 6), s.erle(out, 8, 10)
	for sec := 0; sec < 10; sec++ {
		t.Logf("  ERLE second %d: %.1f dB", sec, s.erle(out, float64(sec), float64(sec+1)))
	}
	t.Logf("ERLE 3-6 s %.1f dB, 8-10 s %.1f dB", early, late)
	if early < 20 {
		t.Errorf("ERLE over 3-6 s %.1f dB, want at least 20", early)
	}
	if late < 25 {
		t.Errorf("ERLE over 8-10 s %.1f dB, want at least 25", late)
	}
}
