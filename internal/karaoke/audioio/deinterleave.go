package audioio

// The helpers here are the whole per-sample logic of the audio callback. The
// malgo backend and the fake backend share them, so what the tests verify is
// what runs on the audio thread. They are pure and allocation-free.

// Deinterleave copies the selected 1-based channels of the interleaved buffer
// src (channels wide) into dst, one slice per selection in selection order.
func Deinterleave(dst [][]float32, src []float32, channels int, sel []int, frames int) {
	for i, c := range sel {
		out := dst[i][:frames]
		idx := c - 1
		for f := range out {
			out[f] = src[idx]
			idx += channels
		}
	}
}

// InterleavePair writes the interleaved stereo buffer src into the 1-based
// channel pair of dst (channels wide) and silences every other channel. Stale
// data in the device buffer therefore never reaches an unrelated output.
func InterleavePair(dst []float32, channels int, pair [2]int, src []float32, frames int) {
	l, r := pair[0]-1, pair[1]-1
	for f := 0; f < frames; f++ {
		frame := dst[f*channels : (f+1)*channels]
		clear(frame)
		frame[l] = src[2*f]
		frame[r] = src[2*f+1]
	}
}

// Downmix writes the mono mix (L+R)/2 of interleaved stereo src into dst.
func Downmix(dst []float32, src []float32, frames int) {
	for f := 0; f < frames; f++ {
		dst[f] = (src[2*f] + src[2*f+1]) / 2
	}
}
