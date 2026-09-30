package audioio

import "testing"

// synthCapture builds an interleaved buffer where the sample of channel c
// (1-based) at frame f is 1000*c + f, so any mix-up is visible in the value.
func synthCapture(channels, frames int) []float32 {
	buf := make([]float32, channels*frames)
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			buf[f*channels+c] = float32(1000*(c+1) + f)
		}
	}
	return buf
}

func TestDeinterleaveExtractsExactlyTheSelectedChannelsFrom18(t *testing.T) {
	const ch, frames = 18, 6
	src := synthCapture(ch, frames)
	dst := [][]float32{make([]float32, frames), make([]float32, frames)}
	Deinterleave(dst, src, ch, []int{1, 2}, frames)
	for i, c := range []int{1, 2} {
		for f := 0; f < frames; f++ {
			if want := float32(1000*c + f); dst[i][f] != want {
				t.Fatalf("input %d frame %d = %v, want %v", c, f, dst[i][f], want)
			}
		}
	}
}

func TestDeinterleavePreservesConfigOrderForUnorderedInputs(t *testing.T) {
	const ch, frames = 18, 4
	src := synthCapture(ch, frames)
	dst := [][]float32{make([]float32, frames), make([]float32, frames)}
	Deinterleave(dst, src, ch, []int{5, 1}, frames)
	if dst[0][0] != 5000 || dst[1][0] != 1000 {
		t.Fatalf("got first frames %v and %v, want 5000 and 1000", dst[0][0], dst[1][0])
	}
	if dst[0][3] != 5003 || dst[1][3] != 1003 {
		t.Fatalf("got last frames %v and %v, want 5003 and 1003", dst[0][3], dst[1][3])
	}
}

func TestInterleavePairWritesOnlyThePairAndSilencesTheRest(t *testing.T) {
	const ch, frames = 20, 5
	for _, pair := range [][2]int{{1, 2}, {3, 4}, {20, 7}} {
		dst := make([]float32, ch*frames)
		for i := range dst {
			dst[i] = 99 // stale data that must be overwritten
		}
		src := make([]float32, 2*frames)
		for f := 0; f < frames; f++ {
			src[2*f] = float32(f + 1)
			src[2*f+1] = -float32(f + 1)
		}
		InterleavePair(dst, ch, pair, src, frames)
		for f := 0; f < frames; f++ {
			for c := 1; c <= ch; c++ {
				got := dst[f*ch+c-1]
				var want float32
				switch c {
				case pair[0]:
					want = float32(f + 1)
				case pair[1]:
					want = -float32(f + 1)
				}
				if got != want {
					t.Fatalf("pair %v frame %d channel %d = %v, want %v", pair, f, c, got, want)
				}
			}
		}
	}
}

func TestDownmixAveragesLeftAndRight(t *testing.T) {
	src := []float32{1, 3, -2, 2, 0.5, 0.25}
	dst := make([]float32, 3)
	Downmix(dst, src, 3)
	want := []float32{2, 0, 0.375}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("dst[%d] = %v, want %v", i, dst[i], want[i])
		}
	}
}
