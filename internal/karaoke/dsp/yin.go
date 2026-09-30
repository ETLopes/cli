package dsp

import (
	"fmt"
	"math"
)

// YINConfig configures a pitch detector.
type YINConfig struct {
	SampleRate float64 // Hz
	Window     int     // samples per analysed frame; 1024 is 64 ms at 16 kHz
	FMin, FMax float64 // Hz; the search range for f0
	// Threshold is the absolute threshold on the normalised difference: the
	// first dip below it is taken as the period. De Cheveigné and Kawahara
	// recommend 0.1 to 0.15; a little higher (the default, 0.2) keeps a
	// singer's vibrato and breathiness voiced. Frames that never dip below it
	// are reported unvoiced.
	Threshold float64
}

// YIN estimates the fundamental frequency of one frame at a time using the
// algorithm of de Cheveigné and Kawahara (2002).
//
// The difference function d(tau) = sum_j (x[j] - x[j+tau])^2 is zero at every
// multiple of the period. Dividing by its running mean (the cumulative mean
// normalised difference, CMND) removes the bias towards small lags that makes
// plain autocorrelation pick octave errors when a strong second harmonic makes
// half the period look nearly as good as the whole one, and lets a single
// absolute threshold decide voicing. The refinement step is a parabola through
// the three raw difference values around the chosen lag.
//
// d(tau) is expanded as e(0) + e(tau) - 2 r(tau) with e the windowed energies
// (prefix sums) and r the cross-correlation, computed with one FFT
// multiplication: about a third of the cost of the direct double loop for the
// 60-1100 Hz range at 16 kHz, which matters with four microphones at 100
// frames per second each.
//
// A YIN is not safe for concurrent use; it owns all its scratch memory and
// allocates nothing per frame.
type YIN struct {
	cfg        YINConfig
	tauMin     int
	tauMax     int
	w          int // integration length: Window - tauMax - 1
	fft        *RealFFT
	a, b       []float64
	sa, sb     []complex128
	energy     []float64 // prefix sums of x^2, length Window+1
	diff, cmnd []float64 // indexed by lag, 0..tauMax+1
}

// NewYIN validates cfg and prepares a detector.
func NewYIN(cfg YINConfig) (*YIN, error) {
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.2
	}
	switch {
	case cfg.SampleRate <= 0:
		return nil, fmt.Errorf("yin sample rate %v must be positive", cfg.SampleRate)
	case cfg.FMin <= 0 || cfg.FMax <= cfg.FMin:
		return nil, fmt.Errorf("yin frequency range %v-%v Hz is not valid", cfg.FMin, cfg.FMax)
	case cfg.FMax >= cfg.SampleRate/4:
		return nil, fmt.Errorf("yin fmax %v Hz leaves fewer than 4 samples per period at %v Hz", cfg.FMax, cfg.SampleRate)
	case cfg.Threshold <= 0 || cfg.Threshold >= 1:
		return nil, fmt.Errorf("yin threshold %v must be between 0 and 1", cfg.Threshold)
	}
	y := &YIN{cfg: cfg}
	y.tauMin = max(2, int(math.Floor(cfg.SampleRate/cfg.FMax)))
	y.tauMax = int(math.Ceil(cfg.SampleRate / cfg.FMin))
	y.w = cfg.Window - y.tauMax - 1
	if y.w < y.tauMax {
		return nil, fmt.Errorf("yin window of %d samples is too short for %v Hz at %v Hz: need at least %d", cfg.Window, cfg.FMin, cfg.SampleRate, 2*y.tauMax+1)
	}
	n := 1
	for n < 2*cfg.Window {
		n <<= 1
	}
	var err error
	if y.fft, err = NewRealFFT(n); err != nil {
		return nil, fmt.Errorf("yin: %w", err)
	}
	y.a, y.b = make([]float64, n), make([]float64, n)
	y.sa, y.sb = make([]complex128, y.fft.Bins()), make([]complex128, y.fft.Bins())
	y.energy = make([]float64, cfg.Window+1)
	y.diff, y.cmnd = make([]float64, y.tauMax+2), make([]float64, y.tauMax+2)
	return y, nil
}

// Window is the number of samples Detect expects per frame.
func (y *YIN) Window() int { return y.cfg.Window }

// Detect analyses one frame of exactly Window samples. It returns the
// fundamental in Hz, or 0 if the frame is unvoiced, and the aperiodicity: the
// normalised difference at the chosen period (or its global minimum when no
// period was accepted), 0 for a perfectly periodic signal and near 1 for noise
// or silence.
func (y *YIN) Detect(frame []float32) (f0, aperiodicity float64) {
	if len(frame) != y.cfg.Window {
		panic(fmt.Sprintf("dsp: yin window is %d samples, frame has %d", y.cfg.Window, len(frame)))
	}
	for i, v := range frame {
		f := float64(v)
		y.a[i] = f
		y.energy[i+1] = y.energy[i] + f*f
		if i < y.w {
			y.b[i] = f
		}
	}
	clear(y.a[len(frame):])
	clear(y.b[y.w:])
	e0 := y.energy[y.w]
	if e0 < 1e-12*float64(y.w) {
		return 0, 1 // digital silence: no period to find
	}

	y.fft.Forward(y.a, y.sa)
	y.fft.Forward(y.b, y.sb)
	for k := range y.sa {
		y.sa[k] *= complex(real(y.sb[k]), -imag(y.sb[k]))
	}
	y.fft.Inverse(y.sa, y.a)
	for tau := 0; tau <= y.tauMax+1; tau++ {
		y.diff[tau] = e0 + (y.energy[tau+y.w] - y.energy[tau]) - 2*y.a[tau]
		if y.diff[tau] < 0 {
			y.diff[tau] = 0 // rounding
		}
	}

	y.cmnd[0] = 1
	var run float64
	for tau := 1; tau <= y.tauMax+1; tau++ {
		run += y.diff[tau]
		if run <= 0 {
			y.cmnd[tau] = 1
		} else {
			y.cmnd[tau] = y.diff[tau] * float64(tau) / run
		}
	}

	// Take the first dip under the threshold and slide to the bottom of it;
	// preferring the smallest lag that qualifies is what avoids choosing a
	// multiple of the period.
	best := -1
	for tau := y.tauMin; tau <= y.tauMax; tau++ {
		if y.cmnd[tau] < y.cfg.Threshold {
			for tau+1 <= y.tauMax && y.cmnd[tau+1] < y.cmnd[tau] {
				tau++
			}
			best = tau
			break
		}
	}
	if best < 0 {
		low := 1.0
		for tau := y.tauMin; tau <= y.tauMax; tau++ {
			low = math.Min(low, y.cmnd[tau])
		}
		return 0, low
	}
	return y.cfg.SampleRate / y.refine(best), y.cmnd[best]
}

// refine returns the sub-sample lag of the minimum near tau by fitting a
// parabola through the raw difference function on either side.
func (y *YIN) refine(tau int) float64 {
	d0, d1, d2 := y.diff[tau-1], y.diff[tau], y.diff[tau+1]
	den := d0 - 2*d1 + d2
	if den <= 0 {
		return float64(tau)
	}
	return float64(tau) + 0.5*(d0-d2)/den
}
