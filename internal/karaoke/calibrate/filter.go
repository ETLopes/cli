package calibrate

import "math"

// highPass filters x in place with a second-order Butterworth high-pass
// (RBJ cookbook biquad, Q = 1/sqrt(2)).
func highPass(x []float64, cutoff, rate float64) {
	w := 2 * math.Pi * cutoff / rate
	alpha := math.Sin(w) / math.Sqrt2
	cw := math.Cos(w)
	a0 := 1 + alpha
	b0, b1, b2 := (1+cw)/2/a0, -(1+cw)/a0, (1+cw)/2/a0
	a1, a2 := -2*cw/a0, (1-alpha)/a0
	var x1, x2, y1, y2 float64
	for i, v := range x {
		y := b0*v + b1*x1 + b2*x2 - a1*y1 - a2*y2
		x2, x1, y2, y1 = x1, v, y1, y
		x[i] = y
	}
}
