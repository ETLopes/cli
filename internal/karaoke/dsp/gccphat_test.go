package dsp

import (
	"math"
	"math/rand"
	"testing"
)

// bandLimitedNoise is white noise low-passed to roughly 0.4 of the sample
// rate, so that a fractional delay is well defined for it.
func bandLimitedNoise(rng *rand.Rand, n int) []float64 {
	raw := make([]float64, n+64)
	for i := range raw {
		raw[i] = rng.NormFloat64()
	}
	out := make([]float64, n)
	for i := range out {
		for k := -32; k <= 32; k++ {
			x := float64(k)
			h := 0.8
			if k != 0 {
				h = math.Sin(0.8*math.Pi*x) / (math.Pi * x)
			}
			h *= 0.5 + 0.5*math.Cos(math.Pi*x/33)
			out[i] += h * raw[i+32+k]
		}
	}
	return out
}

// delayed returns x delayed by d samples (fractional allowed) via a windowed
// sinc interpolator, same length as x.
func delayed(x []float64, d float64) []float64 {
	out := make([]float64, len(x))
	for i := range out {
		pos := float64(i) - d
		c := int(math.Floor(pos))
		for k := c - 24; k <= c+25; k++ {
			if k < 0 || k >= len(x) {
				continue
			}
			u := pos - float64(k)
			s := 1.0
			if u != 0 {
				s = math.Sin(math.Pi*u) / (math.Pi * u)
			}
			s *= 0.5 + 0.5*math.Cos(math.Pi*u/25)
			out[i] += s * x[k]
		}
	}
	return out
}

func addNoiseAtSNR(rng *rand.Rand, x []float64, snrDB float64) []float64 {
	var p float64
	for _, v := range x {
		p += v * v
	}
	p /= float64(len(x))
	sigma := math.Sqrt(p / math.Pow(10, snrDB/10))
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = v + sigma*rng.NormFloat64()
	}
	return out
}

func TestDelayEstimatorRecoversAKnownIntegerDelayExactlyAtTenDecibelsSNR(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	est, err := NewDelayEstimator(800, 8192)
	if err != nil {
		t.Fatal(err)
	}
	ref := bandLimitedNoise(rng, 8192)
	for _, lag := range []int{0, 1, 37, 400, 799, -25} {
		mic := addNoiseAtSNR(rng, delayed(ref, float64(lag)), 10)
		got, err := est.Estimate(f32(ref), f32(mic))
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(got.Delay-float64(lag)) > 0.05 {
			t.Errorf("lag %d: estimated %.3f", lag, got.Delay)
		}
		if got.Confidence < 3 {
			t.Errorf("lag %d: confidence %.2f is too low for a clean detection", lag, got.Confidence)
		}
	}
}

func TestDelayEstimatorRecoversAFractionalDelayWithinATenthOfASample(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	est, _ := NewDelayEstimator(400, 8192)
	worst := 0.0
	for _, lag := range []float64{12.5, 40.3, 100.75, 250.1, 5.9} {
		ref := bandLimitedNoise(rng, 8192)
		mic := addNoiseAtSNR(rng, delayed(ref, lag), 10)
		got, err := est.Estimate(f32(ref), f32(mic))
		if err != nil {
			t.Fatal(err)
		}
		worst = math.Max(worst, math.Abs(got.Delay-lag))
		if math.Abs(got.Delay-lag) > 0.1 {
			t.Errorf("lag %.2f: estimated %.3f", lag, got.Delay)
		}
	}
	t.Logf("worst fractional error %.3f samples", worst)
}

func TestDelayEstimatorSeesThroughAReverberantMusicEcho(t *testing.T) {
	ref := musicReference(6*testRate/2, 3)
	room := newRoom(roomConfig{bulkDelay: 613, rt60: 0.3, seed: 4})
	mic := room.apply(ref)
	est, _ := NewDelayEstimator(2000, len(ref))
	got, err := est.Estimate(f32(ref), f32(mic))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.Delay-613) > 1.5 {
		t.Errorf("estimated %.2f, want the direct path at 613", got.Delay)
	}
}

func TestDelayEstimatorGivesLowConfidenceForUnrelatedSignals(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	est, _ := NewDelayEstimator(400, 8192)
	got, err := est.Estimate(f32(bandLimitedNoise(rng, 8192)), f32(bandLimitedNoise(rng, 8192)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence > 1.6 {
		t.Errorf("confidence %.2f for unrelated noise, want close to 1", got.Confidence)
	}
}

func TestDelayEstimatorRejectsSilenceAndOversizedInput(t *testing.T) {
	est, _ := NewDelayEstimator(100, 1024)
	if _, err := est.Estimate(make([]float32, 1024), make([]float32, 1024)); err == nil {
		t.Error("silence accepted, want an error")
	}
	if _, err := est.Estimate(make([]float32, 5000), make([]float32, 1024)); err == nil {
		t.Error("oversized reference accepted, want an error")
	}
	if _, err := NewDelayEstimator(0, 10); err == nil {
		t.Error("maxLag 0 accepted, want an error")
	}
}
