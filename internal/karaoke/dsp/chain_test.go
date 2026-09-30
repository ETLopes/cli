package dsp

import (
	"math"
	"sync"
	"testing"
)

const (
	chainSettle = 1.0 // seconds of a seeded chain ignored while the suppressor's statistics settle
	chainRoom   = 11  // seed of the first speaker's room in monoThroughTwoSpeakers and calibration
)

// calibrationFloor is what the tool measures at calibration: about 5 s of
// different music through the same room and a seeded canceller, the largest
// level the cleaned output shows with backing only.
func calibrationFloor(t testing.TB) float64 {
	t.Helper()
	floorOnce.Do(func() { floorValue = measureFloor(t) })
	return floorValue
}

var (
	floorOnce  sync.Once
	floorValue float64
)

func measureFloor(t testing.TB) float64 {
	const n = 5 * cancRate
	ref := musicReference(n, 2)
	r := newRoom(roomConfig{bulkDelay: cancBulk, rt60: 0.3, seed: chainRoom})
	mic := sumSignals(r.apply(softClip(ref, 1)), whiteNoise(n, 1e-4, 7))
	c := newTestChain(t, ChainConfig{ImpulseResponse: f32(r.ir)})
	var floor float64
	for _, f := range streamChain(c, ref, mic) {
		if f.Index*GateHop >= int(chainSettle*cancRate) {
			floor = math.Max(floor, f.Level)
		}
	}
	return floor
}

func newTestChain(t testing.TB, cfg ChainConfig) *Chain {
	t.Helper()
	cfg.Tail, cfg.BulkDelay = cancTail, cancBulk
	c, err := NewChain(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// streamChain calls the chain exactly as the live engine will: one 128-sample
// block of the reference and of the microphone at a time.
func streamChain(c *Chain, ref, mic []float64) []Frame {
	r32, m32 := f32(ref), f32(mic)
	var frames []Frame
	for i := 0; i+cancBlock <= len(mic); i += cancBlock {
		if f, ok := c.Process(r32[i:i+cancBlock], m32[i:i+cancBlock]); ok {
			frames = append(frames, f)
		}
	}
	return frames
}

// runScenarioChain runs the production chain (seeded from the scenario's room)
// over s, with the residual floor a calibration on the same room measured.
func runScenarioChain(t testing.TB, s *scenario, seeded bool) []Frame {
	t.Helper()
	cfg := ChainConfig{ResidualFloor: calibrationFloor(t)}
	if seeded {
		cfg.ImpulseResponse = f32(s.ir)
	}
	return streamChain(newTestChain(t, cfg), s.ref, s.mic)
}

// rejectedShare is the fraction of frames from fromSec that are not voiced.
func rejectedShare(frames []Frame, fromSec float64) float64 {
	var n, rej int
	for _, f := range frames {
		if f.Index*GateHop < int(fromSec*cancRate) {
			continue
		}
		n++
		if !f.Voiced {
			rej++
		}
	}
	return float64(rej) / float64(n)
}

// singerHitRate is the fraction of frames whose whole window the singer is
// voicing that the chain accepted within 50 cents of the truth.
func (s *scenario) singerHitRate(frames []Frame, fromSec float64) (rate float64, voiced int) {
	var hit int
	for _, f := range frames {
		st := f.Index * GateHop
		if st < int(fromSec*cancRate) || st+GateWindow > len(s.truth) {
			continue
		}
		ok := true
		for _, tr := range s.truth[st : st+GateWindow] {
			if tr <= 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		voiced++
		if f.Voiced && math.Abs(cents(f.F0, s.truth[st+GateWindow/2])) < 50 {
			hit++
		}
	}
	return float64(hit) / float64(voiced), voiced
}

func singingScenario(onset, seconds float64) *scenario {
	cfg := mainScenario(seconds)
	cfg.singer, cfg.singerAt = 0.1, onset
	return buildScenario(cfg)
}

func TestChainRejectsBackingOnlyThroughTheLiveBlockInterface(t *testing.T) {
	s := buildScenario(mainScenario(10))
	frames := runScenarioChain(t, s, true)
	got := rejectedShare(frames, chainSettle)
	t.Logf("seeded chain, backing only: %.1f%% of %d frames rejected", 100*got, len(frames))
	if got < 0.95 {
		t.Errorf("%.1f%% of backing-only frames rejected, want at least 95%%", 100*got)
	}
}

func TestChainTracksASingerOverBackingThroughTheLiveBlockInterface(t *testing.T) {
	s := singingScenario(6, 12)
	frames := runScenarioChain(t, s, true)
	hit, voiced := s.singerHitRate(frames, 6.5)
	backing := rejectedShare(frames[:6*cancRate/GateHop-10], chainSettle)
	t.Logf("seeded chain with a singer: %.1f%% of %d voiced frames accepted within 50 cents; %.1f%% of the backing-only frames before the singer rejected", 100*hit, voiced, 100*backing)
	if hit < 0.9 {
		t.Errorf("%.1f%% of the singer's frames accepted within 50 cents, want at least 90%%", 100*hit)
	}
	if backing < 0.95 {
		t.Errorf("%.1f%% of the backing-only frames before the singer rejected, want at least 95%%", 100*backing)
	}
}

func TestChainFramesAreTenMillisecondsApartOnTheMicrophoneTimeline(t *testing.T) {
	c := newTestChain(t, ChainConfig{})
	frames := streamChain(c, make([]float64, 4*cancRate), make([]float64, 4*cancRate))
	for i, f := range frames {
		if f.Index != i {
			t.Fatalf("frame %d has index %d", i, f.Index)
		}
	}
	// A frame needs GateWindow cleaned samples plus the suppressor's latency.
	wantFirstCall := (GateWindow+SuppressorLatency)/cancBlock + 1
	if want := (4*cancRate/cancBlock - wantFirstCall + 1) * cancBlock; len(frames) == 0 || len(frames) > want/GateHop+2 {
		t.Errorf("%d frames from 4 s, implausible", len(frames))
	}
	if got := frames[1].Center() - frames[0].Center(); got != GateHop {
		t.Errorf("frames %d samples apart, want %d", got, GateHop)
	}
}

// The unseeded fallback is held to the same targets as the seeded chain with
// regression floors just under what it measures: see fallbackCheck.
func TestChainUnseededFallbackNumbers(t *testing.T) {
	back := runScenarioChain(t, buildScenario(mainScenario(10)), false)
	sung := singingScenario(10, 15)
	frames := runScenarioChain(t, sung, false)
	hit, voiced := sung.singerHitRate(frames, 10.5)
	rej := rejectedShare(back, 7)
	t.Logf("unseeded chain: backing-only %.1f%% rejected over 7-10 s; singer %.1f%% of %d voiced frames accepted", 100*rej, 100*hit, voiced)
	fallbackCheck(t, "backing-only frames rejected", 100*rej, 95, 90, "%")
	fallbackCheck(t, "singer frames accepted within 50 cents", 100*hit, 90, 86, "%")
}

func TestChainDoesNotAllocateInSteadyState(t *testing.T) {
	c := newTestChain(t, ChainConfig{ResidualFloor: 1e-3})
	ref, mic := f32(musicReference(cancBlock*64, 1)), f32(whiteNoise(cancBlock*64, 0.05, 2))
	i := 0
	step := func() {
		j := (i % 64) * cancBlock
		c.Process(ref[j:j+cancBlock], mic[j:j+cancBlock])
		i++
	}
	for k := 0; k < 200; k++ {
		step()
	}
	if n := testing.AllocsPerRun(200, step); n != 0 {
		t.Errorf("Process allocates %.1f times per block, want 0", n)
	}
}

func BenchmarkChain4Mics(b *testing.B) {
	const mics = 4
	chains := make([]*Chain, mics)
	for i := range chains {
		c, err := NewChain(ChainConfig{Tail: 8000, BulkDelay: cancBulk, ResidualFloor: 1e-3})
		if err != nil {
			b.Fatal(err)
		}
		chains[i] = c
	}
	const blocks = 64
	ref := f32(musicReference(cancBlock*blocks, 1))
	mic := make([][]float32, mics)
	for i := range mic {
		mic[i] = f32(sumSignals(musicReference(cancBlock*blocks, 1), whiteNoise(cancBlock*blocks, 0.05, int64(i+2))))
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		j := (n % blocks) * cancBlock
		for i, c := range chains {
			c.Process(ref[j:j+cancBlock], mic[i][j:j+cancBlock])
		}
	}
	perBlock := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	b.ReportMetric(perBlock/(1e9*cancBlock/cancRate), "realtime-fraction")
}
