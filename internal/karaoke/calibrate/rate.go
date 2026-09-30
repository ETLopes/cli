package calibrate

import (
	"fmt"
	"math"

	"github.com/ETLopes/cli/internal/karaoke/dsp"
)

// toPipelineRate brings an impulse response from the device rate to 16 kHz
// without changing where its features sit in time.
//
// The resampler's filter delays everything by GroupDelay output samples, which
// need not be a whole number. The signals a canceller sees are delayed equally
// (reference and mic go through identical converters), so for them it cancels;
// an impulse response is measured against the true playback time, so the delay
// is taken back out by a fractional-sample advance.
func toPipelineRate(ir []float32, rate int) ([]float32, error) {
	r, err := dsp.NewResampler(rate)
	if err != nil {
		return nil, fmt.Errorf("resample the impulse response: %w", err)
	}
	// Zeros after the response flush the converter's filter, whose output
	// otherwise lags the input by the group delay and would cut the tail.
	pad := int(2*r.GroupDelay()*float64(rate)/dsp.PipelineRate) + 64
	in := make([]float32, 0, len(ir)+pad)
	in = append(in, ir...)
	in = append(in, make([]float32, pad)...)
	out := r.Process(nil, in)
	return advance(out, r.GroupDelay(), len(ir)*dsp.PipelineRate/rate), nil
}

// bothToPipelineRate converts the reference and a mic with identical
// converters, so their relative timing is untouched.
func bothToPipelineRate(ref, mic []float32, rate int) (ref16, mic16 []float32, err error) {
	rr, err := dsp.NewResampler(rate)
	if err != nil {
		return nil, nil, fmt.Errorf("resample the reference: %w", err)
	}
	rm, err := dsp.NewResampler(rate)
	if err != nil {
		return nil, nil, fmt.Errorf("resample the microphone: %w", err)
	}
	return rr.Process(nil, ref), rm.Process(nil, mic), nil
}

const advanceHalfWidth = 16

// advance returns n samples of y(i+shift): x moved earlier in time by a
// possibly fractional number of samples, by windowed-sinc interpolation. Good
// for signals band-limited below about 7 kHz at 16 kHz, which is all an
// impulse response measured with a 7.5 kHz sweep holds.
func advance(x []float32, shift float64, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		pos := float64(i) + shift
		base := int(math.Floor(pos))
		frac := pos - float64(base)
		var acc float64
		for j := base - advanceHalfWidth + 1; j <= base+advanceHalfWidth; j++ {
			if j < 0 || j >= len(x) {
				continue
			}
			d := pos - float64(j)
			w := 0.5 + 0.5*math.Cos(math.Pi*d/float64(advanceHalfWidth+1)) // Hann
			s := 1.0
			if frac != 0 || d != 0 {
				s = math.Sin(math.Pi*d) / (math.Pi * d)
			}
			acc += float64(x[j]) * s * w
		}
		out[i] = float32(acc)
	}
	return out
}
