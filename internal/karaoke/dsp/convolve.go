package dsp

// fftConvolve is a full linear convolution through one large FFT. It is for
// one-off offline work such as deconvolving a calibration sweep, where the
// whole signal is in memory and one big transform beats block processing; it
// allocates, so nothing on the live path uses it.
func fftConvolve(x, h []float64) []float64 {
	n := 1
	for n < len(x)+len(h) {
		n <<= 1
	}
	f, _ := NewFFT(n)
	a, b := make([]complex128, n), make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v, 0)
	}
	for i, v := range h {
		b[i] = complex(v, 0)
	}
	f.Forward(a)
	f.Forward(b)
	for i := range a {
		a[i] *= b[i]
	}
	f.Inverse(a)
	out := make([]float64, len(x)+len(h)-1)
	for i := range out {
		out[i] = real(a[i])
	}
	return out
}

// f32 narrows a float64 signal to the float32 the audio path carries.
func f32(x []float64) []float32 {
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = float32(v)
	}
	return out
}
