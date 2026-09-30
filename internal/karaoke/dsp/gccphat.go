package dsp

import (
	"fmt"
	"math"
	"math/cmplx"
)

// DelayEstimator measures how many samples a microphone signal lags a
// reference using the generalised cross-correlation with phase transform
// (Knapp and Carter, 1976).
//
// Plain cross-correlation of a music reference with a reverberant recording is
// smeared by the reference's own spectral colour and by the room. PHAT divides
// the cross-spectrum by its magnitude, keeping only phase, which turns the
// direct-path peak into a sharp spike whatever the material: exactly what is
// wanted to find the bulk delay from an unknown output buffer, device latency
// and speaker-to-mic flight time. Bins whose cross-spectrum is far below the
// average carry only noise, so the whitening is regularised by a floor rather
// than amplifying them.
//
// The peak is refined to a fraction of a sample by a parabola through the
// three correlation values around it.
type DelayEstimator struct {
	maxLag int
	fft    *RealFFT
	a, b   []float64
	sa, sb []complex128
}

// DelayResult is the outcome of one estimate.
type DelayResult struct {
	// Delay is how many samples the microphone lags the reference (positive
	// when the echo arrives later than the reference was played).
	Delay float64
	// Confidence is the peak-to-sidelobe ratio: the correlation peak divided
	// by the largest value more than two samples away from it. Uncorrelated
	// signals give about 1; a clean detection gives many times that.
	Confidence float64
}

// NewDelayEstimator prepares an estimator that searches lags within +-maxLag
// samples on signals of up to maxLen samples each.
func NewDelayEstimator(maxLag, maxLen int) (*DelayEstimator, error) {
	if maxLag < 1 || maxLen < 2 {
		return nil, fmt.Errorf("delay estimator needs maxLag >= 1 and maxLen >= 2, got %d and %d", maxLag, maxLen)
	}
	n := 4
	for n < 2*maxLen+maxLag {
		n <<= 1
	}
	fft, err := NewRealFFT(n)
	if err != nil {
		return nil, fmt.Errorf("delay estimator: %w", err)
	}
	return &DelayEstimator{
		maxLag: maxLag, fft: fft,
		a: make([]float64, n), b: make([]float64, n),
		sa: make([]complex128, fft.Bins()), sb: make([]complex128, fft.Bins()),
	}, nil
}

// Estimate finds the lag of mic relative to ref. Both signals are used from
// their first sample, up to the maxLen the estimator was built for.
func (d *DelayEstimator) Estimate(ref, mic []float32) (DelayResult, error) {
	maxLen := (d.fft.Size() - d.maxLag) / 2
	if len(ref) < 2 || len(mic) < 2 || len(ref) > maxLen || len(mic) > maxLen {
		return DelayResult{}, fmt.Errorf("delay estimation needs 2 to %d samples per signal, got %d and %d", maxLen, len(ref), len(mic))
	}
	load := func(dst []float64, src []float32) {
		for i, v := range src {
			dst[i] = float64(v)
		}
		clear(dst[len(src):])
	}
	load(d.a, ref)
	load(d.b, mic)
	d.fft.Forward(d.a, d.sa)
	d.fft.Forward(d.b, d.sb)

	var meanMag float64
	for k := range d.sa {
		d.sb[k] *= cmplx.Conj(d.sa[k])
		meanMag += cmplx.Abs(d.sb[k])
	}
	meanMag /= float64(len(d.sa))
	if meanMag == 0 {
		return DelayResult{}, fmt.Errorf("delay estimation needs a non-silent reference and microphone signal")
	}
	floor := 1e-3 * meanMag
	for k, v := range d.sb {
		d.sb[k] = v / complex(cmplx.Abs(v)+floor, 0)
	}
	d.fft.Inverse(d.sb, d.a)

	n := d.fft.Size()
	at := func(lag int) float64 { return d.a[(lag+n)%n] }
	best := -d.maxLag
	for lag := -d.maxLag; lag <= d.maxLag; lag++ {
		if at(lag) > at(best) {
			best = lag
		}
	}
	peak := at(best)
	var side float64
	for lag := -d.maxLag; lag <= d.maxLag; lag++ {
		if lag < best-2 || lag > best+2 {
			side = math.Max(side, math.Abs(at(lag)))
		}
	}
	res := DelayResult{Delay: float64(best)}
	if side > 0 {
		res.Confidence = peak / side
	}
	// Parabolic refinement, only when both neighbours exist inside the range.
	if best > -d.maxLag && best < d.maxLag {
		l, r := at(best-1), at(best+1)
		if den := l - 2*peak + r; den < 0 {
			res.Delay += 0.5 * (l - r) / den
		}
	}
	return res, nil
}
