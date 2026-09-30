// Package dsp is the pure-Go signal-processing core of the karaoke tool. It
// turns a raw microphone signal plus the exactly known playback signal into a
// clean pitch track: streaming resampling to 16 kHz, YIN pitch detection,
// GCC-PHAT delay estimation, sweep-based room measurement, a partitioned
// frequency-domain echo canceller, a residual echo suppressor and a gate that
// decides whether a detected pitch is a singer rather than leaked backing
// track.
//
// Nothing here touches an audio device; every component works on plain sample
// slices so it can be exercised with synthetic signals. Internal arithmetic is
// float64 wherever accumulation matters, while the public streaming
// interfaces take and return float32 because that is what the device layer
// speaks.
package dsp

import (
	"fmt"
	"math"
	"math/bits"
)

// FFT is an in-place radix-2 complex transform of one fixed power-of-two size.
//
// It exists in-package rather than as a dependency because the canceller runs
// hundreds of small transforms per second per microphone and needs them to be
// allocation-free; all twiddle tables are built once in NewFFT. An FFT holds
// no mutable state, so one value may be shared between goroutines.
type FFT struct {
	n       int
	twiddle []complex128 // e^{-2*pi*i*k/n} for k < n/2
	rev     []int        // bit-reversal permutation
}

// NewFFT prepares a transform of size n, which must be a power of two >= 2.
func NewFFT(n int) (*FFT, error) {
	if n < 2 || n&(n-1) != 0 {
		return nil, fmt.Errorf("fft size %d is not a power of two >= 2", n)
	}
	f := &FFT{n: n, twiddle: make([]complex128, n/2), rev: make([]int, n)}
	for k := range f.twiddle {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		f.twiddle[k] = complex(c, s)
	}
	shift := bits.UintSize - bits.TrailingZeros(uint(n))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse(uint(i)) >> shift)
	}
	return f, nil
}

// Size is the number of points the transform operates on.
func (f *FFT) Size() int { return f.n }

// Forward computes X[k] = sum x[n] e^{-2*pi*i*n*k/N} in place. x must have
// exactly Size() elements.
func (f *FFT) Forward(x []complex128) {
	f.check(x)
	for i, j := range f.rev {
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= f.n; size <<= 1 {
		half := size >> 1
		step := f.n / size
		for start := 0; start < f.n; start += size {
			for k := 0; k < half; k++ {
				u := x[start+k]
				v := x[start+k+half] * f.twiddle[k*step]
				x[start+k] = u + v
				x[start+k+half] = u - v
			}
		}
	}
}

// Inverse computes the inverse transform in place, including the 1/N scale, so
// Inverse(Forward(x)) returns x.
func (f *FFT) Inverse(x []complex128) {
	f.check(x)
	// ifft(x) = conj(fft(conj(x))) / N: one code path for both directions.
	for i, v := range x {
		x[i] = complex(real(v), -imag(v))
	}
	f.Forward(x)
	s := 1 / float64(f.n)
	for i, v := range x {
		x[i] = complex(real(v)*s, -imag(v)*s)
	}
}

func (f *FFT) check(x []complex128) {
	if len(x) != f.n {
		panic(fmt.Sprintf("dsp: fft of size %d given %d points", f.n, len(x)))
	}
}

// RealFFT transforms real signals of one fixed power-of-two size through a
// single half-size complex FFT (the even/odd packing trick), which halves the
// cost against feeding zeros into the imaginary part. The spectrum carries the
// N/2+1 non-redundant bins, DC through Nyquist.
//
// A RealFFT owns scratch memory, so it is not safe for concurrent use.
type RealFFT struct {
	n       int
	half    *FFT
	buf     []complex128
	twiddle []complex128 // e^{-2*pi*i*k/n} for k <= n/2
}

// NewRealFFT prepares a real transform of size n, a power of two >= 4.
func NewRealFFT(n int) (*RealFFT, error) {
	if n < 4 {
		return nil, fmt.Errorf("real fft size %d is smaller than 4", n)
	}
	half, err := NewFFT(n / 2)
	if err != nil {
		return nil, fmt.Errorf("real fft size %d: %w", n, err)
	}
	r := &RealFFT{n: n, half: half, buf: make([]complex128, n/2), twiddle: make([]complex128, n/2+1)}
	for k := range r.twiddle {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		r.twiddle[k] = complex(c, s)
	}
	return r, nil
}

// Size is the number of real samples per transform.
func (r *RealFFT) Size() int { return r.n }

// Bins is the spectrum length, Size()/2+1.
func (r *RealFFT) Bins() int { return r.n/2 + 1 }

// Forward writes the spectrum of in (Size() samples) to out (Bins() values).
func (r *RealFFT) Forward(in []float64, out []complex128) {
	h := r.n / 2
	if len(in) != r.n || len(out) != h+1 {
		panic(fmt.Sprintf("dsp: real fft of size %d given %d samples and %d bins", r.n, len(in), len(out)))
	}
	for k := 0; k < h; k++ {
		r.buf[k] = complex(in[2*k], in[2*k+1])
	}
	r.half.Forward(r.buf)
	for k := 0; k <= h; k++ {
		zk := r.buf[k%h]
		zc := r.buf[(h-k)%h]
		zc = complex(real(zc), -imag(zc))
		even := (zk + zc) * 0.5
		odd := (zk - zc) * complex(0, -0.5)
		out[k] = even + r.twiddle[k]*odd
	}
}

// Inverse writes the real signal whose spectrum is in (Bins() values) to out
// (Size() samples), including the 1/N scale. The imaginary parts of the DC and
// Nyquist bins are ignored, as they must be for a real signal.
func (r *RealFFT) Inverse(in []complex128, out []float64) {
	h := r.n / 2
	if len(out) != r.n || len(in) != h+1 {
		panic(fmt.Sprintf("dsp: real ifft of size %d given %d bins and %d samples", r.n, len(in), len(out)))
	}
	for k := 0; k < h; k++ {
		xk := in[k]
		xc := in[h-k]
		xc = complex(real(xc), -imag(xc))
		even := (xk + xc) * 0.5
		odd := (xk - xc) * 0.5 * complex(real(r.twiddle[k]), -imag(r.twiddle[k]))
		r.buf[k] = even + complex(0, 1)*odd
	}
	r.half.Inverse(r.buf)
	for k := 0; k < h; k++ {
		out[2*k] = real(r.buf[k])
		out[2*k+1] = imag(r.buf[k])
	}
}
