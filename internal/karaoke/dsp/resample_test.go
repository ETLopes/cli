package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func toneF32(freq, rate float64, n int, amp float64) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return x
}

func rmsF32(x []float32) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

func dbRatio(a, b float64) float64 { return 20 * math.Log10(a/b) }

// resampledGainDB feeds a tone through a fresh converter and reports the
// output-to-input RMS ratio once the filter has settled.
func resampledGainDB(t *testing.T, inRate int, freq float64) float64 {
	t.Helper()
	r, err := NewResampler(inRate)
	if err != nil {
		t.Fatal(err)
	}
	in := toneF32(freq, float64(inRate), inRate, 0.5) // one second
	out := r.Process(nil, in)
	skip := 2 * int(math.Ceil(r.GroupDelay()))
	return dbRatio(rmsF32(out[skip:len(out)-skip]), 0.5/math.Sqrt2)
}

func TestResamplerPassesAOneKilohertzToneWithinATenthOfADecibel(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		if g := resampledGainDB(t, rate, 1000); math.Abs(g) > 0.1 {
			t.Errorf("%d Hz input: 1 kHz gain %.4f dB, want within 0.1 dB", rate, g)
		}
	}
}

func TestResamplerStaysFlatAcrossTheSingingBand(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		for _, f := range []float64{100, 500, 3000, 6000, 7000} {
			if g := resampledGainDB(t, rate, f); math.Abs(g) > 0.1 {
				t.Errorf("%d Hz input: %.0f Hz gain %.4f dB, want within 0.1 dB", rate, f, g)
			}
		}
	}
}

func TestResamplerAttenuatesEverythingAboveEightKilohertzBySixtyDecibels(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		for _, f := range []float64{8100, 8500, 9000, 12000, 15900, 16100, 20000, 21500} {
			if g := resampledGainDB(t, rate, f); g > -60 {
				t.Errorf("%d Hz input: %.0f Hz gain %.2f dB, want at most -60 dB", rate, f, g)
			}
		}
	}
}

func TestResamplerGivesTheSameResultForAnyChunking(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		rng := rand.New(rand.NewSource(7))
		in := make([]float32, 20000)
		for i := range in {
			in[i] = float32(rng.NormFloat64())
		}
		whole, _ := NewResampler(rate)
		want := whole.Process(nil, in)

		chunked, _ := NewResampler(rate)
		var got []float32
		for pos := 0; pos < len(in); {
			n := min(1+rng.Intn(700), len(in)-pos)
			got = chunked.Process(got, in[pos:pos+n])
			pos += n
		}
		if len(got) != len(want) {
			t.Fatalf("%d Hz: chunked produced %d samples, one-shot %d", rate, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%d Hz: sample %d differs: chunked %v, one-shot %v", rate, i, got[i], want[i])
			}
		}
	}
}

func TestResamplerProducesTheRightNumberOfSamples(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		r, _ := NewResampler(rate)
		out := r.Process(nil, make([]float32, rate))
		if len(out) != PipelineRate {
			t.Errorf("%d Hz: one second in gave %d samples out, want %d", rate, len(out), PipelineRate)
		}
	}
}

func TestResamplerGroupDelayLocatesAnImpulse(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		r, _ := NewResampler(rate)
		const at = 3000 // input sample index of the impulse
		in := make([]float32, 8000)
		in[at] = 1
		out := r.Process(nil, in)
		peak := 0
		for i, v := range out {
			if math.Abs(float64(v)) > math.Abs(float64(out[peak])) {
				peak = i
			}
		}
		want := float64(at)*PipelineRate/float64(rate) + r.GroupDelay()
		if math.Abs(float64(peak)-want) > 1 {
			t.Errorf("%d Hz: impulse peaks at output %d, group delay predicts %.2f", rate, peak, want)
		}
	}
}

func TestResamplerAt16kHzPassesSamplesThroughUnchanged(t *testing.T) {
	r, err := NewResampler(PipelineRate)
	if err != nil {
		t.Fatal(err)
	}
	in := []float32{1, 2, 3}
	out := r.Process(nil, in)
	if len(out) != 3 || out[0] != 1 || out[2] != 3 || r.GroupDelay() != 0 {
		t.Errorf("got %v with delay %v, want a straight copy", out, r.GroupDelay())
	}
}

func TestResamplerRejectsRatesBelowThePipelineRate(t *testing.T) {
	if _, err := NewResampler(8000); err == nil {
		t.Error("NewResampler(8000) succeeded, want an error")
	}
	if _, err := NewResampler(47999); err == nil {
		t.Error("NewResampler(47999) succeeded, want an error for an unreasonable ratio")
	}
}

func TestResamplerDoesNotAllocateInSteadyState(t *testing.T) {
	for _, rate := range []int{48000, 44100} {
		r, _ := NewResampler(rate)
		in := toneF32(440, float64(rate), 480, 0.3)
		buf := make([]float32, 0, 1024)
		buf = r.Process(buf[:0], in) // warm-up
		if n := testing.AllocsPerRun(50, func() { buf = r.Process(buf[:0], in) }); n != 0 {
			t.Errorf("%d Hz: %v allocations per run, want 0", rate, n)
		}
	}
}
