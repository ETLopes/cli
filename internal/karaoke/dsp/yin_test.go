package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func newTestYIN(t testing.TB) *YIN {
	t.Helper()
	y, err := NewYIN(YINConfig{SampleRate: testRate, Window: 1024, FMin: 60, FMax: 1100})
	if err != nil {
		t.Fatal(err)
	}
	return y
}

func cents(got, want float64) float64 { return 1200 * math.Log2(got/want) }

func TestYINFindsPureTonesFromEightyHertzToAKilohertzWithinFiveCents(t *testing.T) {
	y := newTestYIN(t)
	worst := 0.0
	defer func() { t.Logf("worst error %.2f cents", worst) }()
	for _, f := range []float64{80, 100, 130.8, 196, 261.6, 330, 440, 587, 700, 880, 1000} {
		frame := f32(harmonicVoice(1024, constF0(f), []float64{0.6}, false))
		got, ap := y.Detect(frame)
		worst = math.Max(worst, math.Abs(cents(got, f)))
		if got == 0 || math.Abs(cents(got, f)) > 5 {
			t.Errorf("pure %.1f Hz: detected %.3f Hz (%.1f cents off, aperiodicity %.3f)", f, got, cents(got, f), ap)
		}
	}
}

func TestYINFindsHarmonicTonesWithinFiveCents(t *testing.T) {
	y := newTestYIN(t)
	worst := 0.0
	defer func() { t.Logf("worst error %.2f cents", worst) }()
	for _, f := range []float64{80, 110, 165, 220, 330, 440, 659, 880, 1000} {
		frame := f32(harmonicVoice(1024, constF0(f), []float64{1, 0.7, 0.5, 0.3, 0.2}, true))
		got, ap := y.Detect(frame)
		worst = math.Max(worst, math.Abs(cents(got, f)))
		if got == 0 || math.Abs(cents(got, f)) > 5 {
			t.Errorf("harmonic %.1f Hz: detected %.3f Hz (%.1f cents off, aperiodicity %.3f)", f, got, cents(got, f), ap)
		}
	}
}

func TestYINTracksTheMeanOfAVibratoOfFiftyCentsAtFiveHertz(t *testing.T) {
	y := newTestYIN(t)
	const centre = 262.0
	sig := f32(harmonicVoice(2*testRate, vibratoF0(centre, 50, 5), []float64{1, 0.6, 0.3}, true))
	var sum float64
	var count int
	for start := 0; start+1024 <= len(sig); start += 160 {
		f0, _ := y.Detect(sig[start : start+1024])
		if f0 == 0 {
			t.Fatalf("frame at %d unvoiced under vibrato", start)
		}
		sum += cents(f0, centre)
		count++
	}
	// 2 s is exactly ten vibrato cycles, so the mean over frames is unbiased.
	mean := sum / float64(count)
	t.Logf("mean vibrato pitch error %.2f cents", mean)
	if math.Abs(mean) > 10 {
		t.Errorf("mean pitch off by %.2f cents, want within 10", mean)
	}
}

func TestYINCallsSilenceAndWhiteNoiseUnvoiced(t *testing.T) {
	y := newTestYIN(t)
	if f0, ap := y.Detect(make([]float32, 1024)); f0 != 0 || ap < 0.99 {
		t.Errorf("silence: f0 %v aperiodicity %v, want 0 and ~1", f0, ap)
	}
	rng := rand.New(rand.NewSource(5))
	voiced := 0
	var minAp = 1.0
	for i := 0; i < 200; i++ {
		frame := make([]float32, 1024)
		for j := range frame {
			frame[j] = float32(0.3 * rng.NormFloat64())
		}
		f0, ap := y.Detect(frame)
		if f0 != 0 {
			voiced++
		}
		minAp = math.Min(minAp, ap)
	}
	if voiced > 2 {
		t.Errorf("%d of 200 white-noise frames came out voiced, want at most 2", voiced)
	}
	if minAp < 0.3 {
		t.Errorf("white noise aperiodicity dipped to %.3f, want it high", minAp)
	}
}

func TestYINDoesNotJumpAnOctaveOnAWeakFundamental(t *testing.T) {
	y := newTestYIN(t)
	rng := rand.New(rand.NewSource(6))
	right, total := 0, 0
	for _, f := range []float64{100, 130, 165, 220, 262, 330, 392} {
		clean := harmonicVoice(testRate/2, constF0(f), []float64{0.05, 1, 0.9}, false)
		for i := range clean {
			clean[i] += 0.02 * rng.NormFloat64()
		}
		sig := f32(clean)
		for start := 0; start+1024 <= len(sig); start += 160 {
			got, _ := y.Detect(sig[start : start+1024])
			total++
			if got != 0 && math.Abs(cents(got, f)) < 50 {
				right++
			}
		}
	}
	rate := float64(right) / float64(total)
	t.Logf("right-octave rate %.1f%%", 100*rate)
	if rate < 0.95 {
		t.Errorf("%.1f%% of frames had the right octave, want at least 95%%", 100*rate)
	}
}

func TestYINRejectsBadConfigurations(t *testing.T) {
	bad := []YINConfig{
		{SampleRate: 0, Window: 1024, FMin: 60, FMax: 1100},
		{SampleRate: 16000, Window: 1024, FMin: 0, FMax: 1100},
		{SampleRate: 16000, Window: 1024, FMin: 500, FMax: 400},
		{SampleRate: 16000, Window: 1024, FMin: 60, FMax: 5000},
		{SampleRate: 16000, Window: 300, FMin: 60, FMax: 1100},
		{SampleRate: 16000, Window: 1024, FMin: 60, FMax: 1100, Threshold: 1.5},
	}
	for i, cfg := range bad {
		if _, err := NewYIN(cfg); err == nil {
			t.Errorf("config %d (%+v) accepted, want an error", i, cfg)
		}
	}
}

func TestYINDoesNotAllocateInSteadyState(t *testing.T) {
	y := newTestYIN(t)
	frame := f32(harmonicVoice(1024, constF0(220), []float64{1, 0.5}, false))
	y.Detect(frame)
	if n := testing.AllocsPerRun(50, func() { y.Detect(frame) }); n != 0 {
		t.Errorf("%v allocations per frame, want 0", n)
	}
}
