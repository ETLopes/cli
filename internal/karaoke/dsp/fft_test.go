package dsp

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

func naiveDFT(x []complex128) []complex128 {
	n := len(x)
	out := make([]complex128, n)
	for k := range out {
		for t, v := range x {
			out[k] += v * cmplx.Exp(complex(0, -2*math.Pi*float64(t*k)/float64(n)))
		}
	}
	return out
}

func randomComplex(rng *rand.Rand, n int) []complex128 {
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(rng.NormFloat64(), rng.NormFloat64())
	}
	return x
}

func TestFFTMatchesANaiveDFTOnRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{2, 4, 8, 64, 256} {
		f, err := NewFFT(n)
		if err != nil {
			t.Fatal(err)
		}
		x := randomComplex(rng, n)
		want := naiveDFT(x)
		f.Forward(x)
		for k := range x {
			if cmplx.Abs(x[k]-want[k]) > 1e-9*float64(n) {
				t.Fatalf("n=%d bin %d: got %v want %v", n, k, x[k], want[k])
			}
		}
	}
}

func TestFFTRoundTripRestoresTheInput(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	f, _ := NewFFT(512)
	x := randomComplex(rng, 512)
	orig := append([]complex128(nil), x...)
	f.Forward(x)
	f.Inverse(x)
	for i := range x {
		if cmplx.Abs(x[i]-orig[i]) > 1e-5 {
			t.Fatalf("sample %d: got %v want %v", i, x[i], orig[i])
		}
	}
}

func TestFFTRejectsSizesThatAreNotPowersOfTwo(t *testing.T) {
	for _, n := range []int{0, 1, 3, 100, -4} {
		if _, err := NewFFT(n); err == nil {
			t.Errorf("NewFFT(%d) succeeded, want an error", n)
		}
	}
	if _, err := NewRealFFT(2); err == nil {
		t.Error("NewRealFFT(2) succeeded, want an error")
	}
	if _, err := NewRealFFT(12); err == nil {
		t.Error("NewRealFFT(12) succeeded, want an error")
	}
}

func TestRealFFTMatchesTheComplexTransformOfTheSameSignal(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, n := range []int{4, 16, 256} {
		rf, err := NewRealFFT(n)
		if err != nil {
			t.Fatal(err)
		}
		in := make([]float64, n)
		cx := make([]complex128, n)
		for i := range in {
			in[i] = rng.NormFloat64()
			cx[i] = complex(in[i], 0)
		}
		want := naiveDFT(cx)
		got := make([]complex128, rf.Bins())
		rf.Forward(in, got)
		for k := range got {
			if cmplx.Abs(got[k]-want[k]) > 1e-9*float64(n) {
				t.Fatalf("n=%d bin %d: got %v want %v", n, k, got[k], want[k])
			}
		}
	}
}

func TestRealFFTRoundTripRestoresTheInput(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	rf, _ := NewRealFFT(512)
	in := make([]float64, 512)
	for i := range in {
		in[i] = rng.NormFloat64()
	}
	spec := make([]complex128, rf.Bins())
	out := make([]float64, 512)
	rf.Forward(in, spec)
	rf.Inverse(spec, out)
	for i := range in {
		if math.Abs(in[i]-out[i]) > 1e-5 {
			t.Fatalf("sample %d: got %v want %v", i, out[i], in[i])
		}
	}
}

func TestFFTDoesNotAllocateWhenReused(t *testing.T) {
	rf, _ := NewRealFFT(256)
	in := make([]float64, 256)
	spec := make([]complex128, rf.Bins())
	if n := testing.AllocsPerRun(50, func() {
		rf.Forward(in, spec)
		rf.Inverse(spec, in)
	}); n != 0 {
		t.Errorf("real fft allocated %v times per run, want 0", n)
	}
}
