package audioio

import "testing"

// stereoRamp returns n stereo frames where frame i is (i+1, -(i+1)).
func stereoRamp(n int) []float32 {
	s := make([]float32, 2*n)
	for i := 0; i < n; i++ {
		s[2*i] = float32(i + 1)
		s[2*i+1] = -float32(i + 1)
	}
	return s
}

func TestBufferSourcePlaysTheTrackThenReturnsShortReads(t *testing.T) {
	src := NewBufferSource(stereoRamp(5))
	dst := make([]float32, 6) // 3 frames
	if n := src.Read(dst); n != 3 {
		t.Fatalf("first Read = %d frames, want 3", n)
	}
	if dst[4] != 3 || dst[5] != -3 {
		t.Fatalf("frame 3 = (%v, %v), want (3, -3)", dst[4], dst[5])
	}
	if src.Done() {
		t.Fatal("Done before the end")
	}
	if n := src.Read(dst); n != 2 {
		t.Fatalf("second Read = %d frames, want 2", n)
	}
	if !src.Done() {
		t.Fatal("not Done at the end")
	}
	if n := src.Read(dst); n != 0 {
		t.Fatalf("Read after end = %d, want 0", n)
	}
	if src.Position() != 5 {
		t.Fatalf("Position = %d, want 5", src.Position())
	}
}

func TestBufferSourcePlaysSilenceWhilePausedAndKeepsItsPlace(t *testing.T) {
	src := NewBufferSource(stereoRamp(10))
	dst := make([]float32, 8) // 4 frames
	src.Read(dst)
	src.Pause()
	for i := range dst {
		dst[i] = 9
	}
	if n := src.Read(dst); n != 4 {
		t.Fatalf("paused Read = %d frames, want a full 4 so it is not mistaken for the end", n)
	}
	for i, v := range dst {
		if v != 0 {
			t.Fatalf("paused dst[%d] = %v, want silence", i, v)
		}
	}
	if src.Position() != 4 {
		t.Fatalf("Position moved while paused: %d", src.Position())
	}
	src.Resume()
	src.Read(dst)
	if dst[0] != 5 {
		t.Fatalf("after Resume first sample = %v, want 5", dst[0])
	}
}

func TestBufferSourceSeeksAndClamps(t *testing.T) {
	src := NewBufferSource(stereoRamp(10))
	src.Seek(7)
	dst := make([]float32, 2)
	src.Read(dst)
	if dst[0] != 8 {
		t.Fatalf("after Seek(7) got %v, want 8", dst[0])
	}
	src.Seek(-3)
	if src.Position() != 0 {
		t.Fatalf("Seek(-3) -> %d, want 0", src.Position())
	}
	src.Seek(99)
	if src.Position() != 10 || !src.Done() {
		t.Fatalf("Seek(99) -> %d done=%v, want 10 true", src.Position(), src.Done())
	}
}
