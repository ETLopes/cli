package dsp

import (
	"math"
	"testing"
)

func correlation(a, b []float64) float64 {
	var ab, aa, bb float64
	for i := range a {
		ab += a[i] * b[i]
		aa += a[i] * a[i]
		bb += b[i] * b[i]
	}
	return ab / math.Sqrt(aa*bb)
}

func f64(x []float32) []float64 {
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = float64(v)
	}
	return out
}

// recordThrough plays the sweep through a room and appends silence for the tail.
func recordThrough(sw *Sweep, r *room, noise float64, seed int64) []float32 {
	rec := fftConvolve(f64(sw.Signal()), r.ir)
	n := len(rec)
	return f32(sumSignals(rec, whiteNoise(n, noise, seed)))
}

func TestSweepDeconvolutionRecoversAKnownImpulseResponse(t *testing.T) {
	sw, err := NewSweep(20, 7900, 2, testRate)
	if err != nil {
		t.Fatal(err)
	}
	r := newRoom(roomConfig{bulkDelay: 300, rt60: 0.3, seed: 21})
	rec := recordThrough(sw, r, 1e-4, 22)
	ir, err := sw.ImpulseResponse(rec, len(r.ir))
	if err != nil {
		t.Fatal(err)
	}
	c := correlation(f64(ir), r.ir)
	t.Logf("recovered impulse response correlation %.4f", c)
	if c < 0.98 {
		t.Errorf("correlation %.4f, want at least 0.98", c)
	}
}

func TestSweepKeepsHarmonicDistortionOutOfTheLinearResponse(t *testing.T) {
	sw, _ := NewSweep(20, 7900, 2, testRate)
	r := newRoom(roomConfig{bulkDelay: 300, rt60: 0.3, seed: 21})
	// A hard-driven speaker: strong odd and even harmonics.
	played := f64(sw.Signal())
	distorted := make([]float64, len(played))
	for i, v := range played {
		distorted[i] = math.Tanh(2.5*v) + 0.2*v*v
	}
	rec := f32(fftConvolve(distorted, r.ir))
	ir, _ := sw.ImpulseResponse(rec, len(r.ir))
	if c := correlation(f64(ir), r.ir); c < 0.9 {
		t.Errorf("distorted correlation %.3f, want the linear response to survive (>= 0.9)", c)
	}
}

func TestEchoTailMatchesTheDecayTimeOfTheRoom(t *testing.T) {
	sw, _ := NewSweep(20, 7900, 2, testRate)
	for _, rt60 := range []float64{0.2, 0.3, 0.45} {
		r := newRoom(roomConfig{bulkDelay: 200, rt60: rt60, seed: 31})
		rec := recordThrough(sw, r, 3e-4, 32)
		ir, _ := sw.ImpulseResponse(rec, len(r.ir)+int(0.3*testRate))
		onset, tail := EchoTail(ir, testRate)
		want := rt60 * 40 / 60 // -40 dB of an exponential decay whose -60 dB point is rt60
		t.Logf("rt60 %.2f: onset %d tail %.3f s, expected %.3f s", rt60, onset, tail, want)
		if math.Abs(float64(onset)-200) > 2 {
			t.Errorf("rt60 %.2f: direct sound found at %d, want 200", rt60, onset)
		}
		if math.Abs(tail-want) > 0.2*want {
			t.Errorf("rt60 %.2f: tail %.3f s, want %.3f s within 20%%", rt60, tail, want)
		}
	}
}

func TestSweepSweepsUpwardsExponentially(t *testing.T) {
	sw, _ := NewSweep(100, 6400, 3, testRate)
	x := f64(sw.Signal())
	crossings := func(from, to int) float64 {
		c := 0
		for i := from + 1; i < to; i++ {
			if (x[i-1] < 0) != (x[i] < 0) {
				c++
			}
		}
		return float64(c) / 2 / (float64(to-from) / testRate)
	}
	n := len(x)
	start, mid, end := crossings(n/100, n/50), crossings(n/2-200, n/2+200), crossings(n-n/50, n-n/100)
	if math.Abs(start-100) > 20 || math.Abs(end-6400) > 800 {
		t.Errorf("sweep runs from ~%.0f Hz to ~%.0f Hz, want about 100 to 6400", start, end)
	}
	if math.Abs(mid-800) > 100 { // geometric middle of 100..6400 is 800 Hz
		t.Errorf("sweep passes ~%.0f Hz halfway, want about 800 (exponential)", mid)
	}
}

func TestSweepRejectsInvalidParameters(t *testing.T) {
	bad := [][4]float64{{0, 1000, 1, 16000}, {1000, 500, 1, 16000}, {20, 8000, 1, 16000}, {20, 1000, 0, 16000}, {20, 1000, 1, 0}}
	for _, b := range bad {
		if _, err := NewSweep(b[0], b[1], b[2], b[3]); err == nil {
			t.Errorf("NewSweep%v accepted, want an error", b)
		}
	}
	sw, _ := NewSweep(20, 1000, 0.5, testRate)
	if _, err := sw.ImpulseResponse(make([]float32, 10), 100); err == nil {
		t.Error("recording shorter than the sweep accepted, want an error")
	}
	if _, err := sw.ImpulseResponse(make([]float32, 9000), 0); err == nil {
		t.Error("zero length accepted, want an error")
	}
}
