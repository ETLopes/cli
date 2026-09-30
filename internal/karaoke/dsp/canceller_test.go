package dsp

import (
	"math"
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
	ir                                   []float64 // the room's true impulse response
}

type scenarioConfig struct {
	seconds  float64
	rt60     float64
	drive    float64 // soft-clip drive; 0 disables
	noise    float64 // noise floor sigma
	singer   float64 // singer level; 0 for none
	singerAt float64 // seconds before the singer starts
	seed     int64
	// The reference is silent in [silentFrom, silentTo) seconds, if set.
	silentFrom, silentTo float64
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
	for i := int(c.silentFrom * cancRate); i < int(c.silentTo*cancRate); i++ {
		s.ref[i] = 0
	}
	r := newRoom(roomConfig{bulkDelay: cancBulk, rt60: c.rt60, seed: c.seed + 10})
	s.ir = r.ir
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
	out, _ := runCanceller(t, newTestCanceller(t), s.ref, s.mic)
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

func (s *scenario) seed(c *Canceller) { c.SeedImpulseResponse(f32(s.ir)) }

// singerGainDB is how much of the singer survives in out: the level of the
// projection of out onto the singer, in dB, over [from, to) seconds.
func (s *scenario) singerGainDB(out []float64, from, to float64) float64 {
	var os, ss float64
	for i := int(from * cancRate); i < int(to*cancRate); i++ {
		os += out[i] * s.singer[i]
		ss += s.singer[i] * s.singer[i]
	}
	return 20 * math.Log10(os/ss)
}

// pitchHitRate is the fraction of fully voiced 1024-sample frames in
// [from, to) seconds whose YIN estimate on out is within 50 cents of the truth.
func (s *scenario) pitchHitRate(t testing.TB, out []float64, from, to float64) float64 {
	y := newTestYIN(t)
	var voiced, hit int
	for st := int(from * cancRate); st+1024 <= int(to*cancRate); st += 512 {
		ok := true
		for _, f := range s.truth[st : st+1024] {
			if f <= 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		voiced++
		if f0, _ := y.Detect(f32(out[st : st+1024])); f0 > 0 && math.Abs(cents(f0, s.truth[st+512])) < 50 {
			hit++
		}
	}
	if voiced == 0 {
		t.Fatal("no voiced frames")
	}
	return float64(hit) / float64(voiced)
}

func TestCancellerSeededFromTheTrueRoomCancelsWithinTheFirstSecond(t *testing.T) {
	s := buildScenario(mainScenario(3))
	c := newTestCanceller(t)
	s.seed(c)
	out, _ := runCanceller(t, c, s.ref, s.mic)
	got := s.erle(out, 0, 1)
	t.Logf("seeded ERLE over the first second %.1f dB", got)
	if got < 20 {
		t.Errorf("ERLE %.1f dB, want at least 20", got)
	}
}

func testDoubleTalk(t *testing.T, seeded bool) {
	cfg := mainScenario(12)
	cfg.singer, cfg.singerAt = 0.1, 6
	s := buildScenario(cfg)
	c := newTestCanceller(t)
	if seeded {
		s.seed(c)
	}
	var echoPow, bgPow, fgPow float64
	out, _ := runCanceller(t, c, s.ref, s.mic, func(i int) {
		if i < int(6.5*cancRate) {
			return
		}
		for j := 0; j < cancBlock; j++ {
			echoPow += s.echo[i+j] * s.echo[i+j]
			d := s.echo[i+j] - c.bg.y[j]
			bgPow += d * d
			d = s.echo[i+j] - c.fg.y[j]
			fgPow += d * d
		}
	})
	t.Logf("background-path ERLE during singing (diagnostic, before the block's update) %.1f dB, foreground %.1f dB", 10*math.Log10(echoPow/bgPow), 10*math.Log10(echoPow/fgPow))
	before, during := s.erle(out, 4, 6), s.erle(out, 6.5, 12)
	gain := s.singerGainDB(out, 6.5, 12)
	hit := s.pitchHitRate(t, out, 6.5, 12)
	t.Logf("seeded=%v: ERLE before %.1f dB, during singing %.1f dB, singer gain %.2f dB, pitch hits %.1f%%", seeded, before, during, gain, 100*hit)
	if during < 15 {
		t.Errorf("ERLE during double talk %.1f dB, want at least 15", during)
	}
	if math.Abs(gain) > 1 {
		t.Errorf("singer level changed by %.2f dB, want within 1", gain)
	}
	if hit < 0.9 {
		t.Errorf("pitch tracked in %.1f%% of voiced frames, want at least 90%%", 100*hit)
	}
}

func TestCancellerKeepsCancellingAndPreservesTheSingerDuringDoubleTalkAfterSeeding(t *testing.T) {
	testDoubleTalk(t, true)
}

func TestCancellerKeepsCancellingAndPreservesTheSingerDuringDoubleTalkAfterConverging(t *testing.T) {
	testDoubleTalk(t, false)
}

func TestCancellerRecoversFromAnEchoPathChange(t *testing.T) {
	const swapAt = 3.0
	s := buildScenario(mainScenario(12))
	r2 := newRoom(roomConfig{bulkDelay: cancBulk, rt60: 0.3, seed: 99})
	echo2 := r2.apply(softClip(s.ref, 1.0))
	for i := int(swapAt * cancRate); i < len(s.echo); i++ {
		s.mic[i] += echo2[i] - s.echo[i]
		s.echo[i] = echo2[i]
	}
	c := newTestCanceller(t)
	s.seed(c)
	out, _ := runCanceller(t, c, s.ref, s.mic)
	t.Logf("ERLE just before the change %.1f dB", s.erle(out, swapAt-1, swapAt))
	recovered := -1.0
	for w := swapAt; w+1 <= 12; w += 0.5 {
		e := s.erle(out, w, w+1)
		if e >= 15 && recovered < 0 {
			recovered = w + 1 - swapAt
		}
	}
	t.Logf("back to 15 dB (1 s window) %.1f s after the change", recovered)
	if recovered < 0 || recovered > 5 {
		t.Errorf("recovery took %.1f s, want at most 5", recovered)
	}
}

func TestCancellerSurvivesSilenceOnTheReference(t *testing.T) {
	cfg := mainScenario(10)
	cfg.silentFrom, cfg.silentTo = 4, 6
	s := buildScenario(cfg)
	c := newTestCanceller(t)
	s.seed(c)
	out, _ := runCanceller(t, c, s.ref, s.mic)
	from, to := int(4.5*cancRate), int(6*cancRate)
	if rms(out[from:to]) > 2*rms(s.mic[from:to])+1e-6 {
		t.Errorf("output rms %.2e in silence, mic %.2e: diverged", rms(out[from:to]), rms(s.mic[from:to]))
	}
	before, after := s.erle(out, 3, 4), s.erle(out, 6.25, 7.25)
	t.Logf("ERLE before silence %.1f dB, just after %.1f dB", before, after)
	if after < before-6 || after < 20 {
		t.Errorf("ERLE %.1f dB after silence (%.1f before), cancellation did not resume", after, before)
	}
}

func TestCancellerColdStartWithASingerDoesNotDiverge(t *testing.T) {
	cfg := mainScenario(10)
	cfg.singer, cfg.singerAt = 0.1, 0
	s := buildScenario(cfg)
	c := newTestCanceller(t)
	out, _ := runCanceller(t, c, s.ref, s.mic)
	for i, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite output at sample %d", i)
		}
	}
	for sec := 0; sec < 10; sec++ {
		a, b := sec*cancRate, (sec+1)*cancRate
		if rms(out[a:b]) > 1.2*rms(s.mic[a:b]) {
			t.Errorf("second %d: output rms %.3f louder than mic %.3f", sec, rms(out[a:b]), rms(s.mic[a:b]))
		}
	}
	gain := s.singerGainDB(out, 1, 10)
	t.Logf("cold start with a singer: ERLE 6-10 s %.1f dB, singer gain %.2f dB", s.erle(out, 6, 10), gain)
	if math.Abs(gain) > 1.5 {
		t.Errorf("singer level changed by %.2f dB, want within 1.5", gain)
	}
}

func TestCancellerHandlesAStereoReferenceDownmixedToMono(t *testing.T) {
	n := 10 * cancRate
	left := musicReference(n, 1)
	other := musicReference(n, 2)
	right := make([]float64, n)
	for i := range right {
		right[i] = 0.7*left[i] + 0.3*other[i]
	}
	rl := newRoom(roomConfig{bulkDelay: cancBulk, rt60: 0.3, seed: 11})
	rr := newRoom(roomConfig{bulkDelay: cancBulk + 20, rt60: 0.3, seed: 12})
	echo := sumSignals(rl.apply(softClip(left, 1)), rr.apply(softClip(right, 1)))
	mono := make([]float64, n)
	for i := range mono {
		mono[i] = (left[i] + right[i]) / 2
	}
	s := &scenario{ref: mono, echo: echo, singer: make([]float64, n)}
	s.mic = sumSignals(echo, whiteNoise(n, 1e-4, 5))
	out, _ := runCanceller(t, newTestCanceller(t), s.ref, s.mic)
	got := s.erle(out, 6, 10)
	t.Logf("stereo-to-mono ERLE over 6-10 s %.1f dB", got)
	if got < 10 {
		t.Errorf("ERLE %.1f dB, want at least 10", got)
	}
}

func TestCancellerDoesNotAllocateInSteadyState(t *testing.T) {
	c := newTestCanceller(t)
	ref, mic, out, echo := make([]float32, cancBlock), make([]float32, cancBlock), make([]float32, cancBlock), make([]float32, cancBlock)
	for i := range ref {
		ref[i], mic[i] = float32(math.Sin(float64(i)*0.3)), float32(math.Sin(float64(i)*0.3+1))
	}
	for i := 0; i < 50; i++ {
		c.Process(ref, mic, out, echo)
	}
	if n := testing.AllocsPerRun(50, func() { c.Process(ref, mic, out, echo) }); n != 0 {
		t.Errorf("Process allocates %.0f times per block, want 0", n)
	}
}

func BenchmarkCancellerProcess500msTail(b *testing.B) {
	c, err := NewCanceller(CancellerConfig{Tail: 8000, BulkDelay: cancBulk})
	if err != nil {
		b.Fatal(err)
	}
	ref, mic, out := f32(musicReference(cancBlock*64, 1)), f32(whiteNoise(cancBlock*64, 0.1, 2)), make([]float32, cancBlock)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := (i % 64) * cancBlock
		c.Process(ref[j:j+cancBlock], mic[j:j+cancBlock], out, nil)
	}
	perBlock := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	b.ReportMetric(perBlock/(1e9*cancBlock/cancRate), "realtime-fraction")
}
