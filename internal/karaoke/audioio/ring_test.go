package audioio

import (
	"sync"
	"testing"
)

func seq(from, n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(from + i)
	}
	return s
}

func TestRingReturnsWrittenSamplesInOrder(t *testing.T) {
	r := NewRing(8)
	if got := r.Write(seq(0, 5)); got != 5 {
		t.Fatalf("Write = %d, want 5", got)
	}
	dst := make([]float32, 8)
	if n := r.Read(dst); n != 5 {
		t.Fatalf("Read = %d, want 5", n)
	}
	for i := 0; i < 5; i++ {
		if dst[i] != float32(i) {
			t.Fatalf("dst[%d] = %v, want %d", i, dst[i], i)
		}
	}
}

func TestRingRoundsCapacityUpToPowerOfTwo(t *testing.T) {
	if c := NewRing(5).Cap(); c != 8 {
		t.Fatalf("Cap = %d, want 8", c)
	}
	if c := NewRing(0).Cap(); c != 1 {
		t.Fatalf("Cap = %d, want 1", c)
	}
}

func TestRingWrapsAroundTheEndOfItsBuffer(t *testing.T) {
	r := NewRing(8)
	dst := make([]float32, 8)
	next, want := 0, 0
	for round := 0; round < 10; round++ {
		r.Write(seq(next, 5))
		next += 5
		n := r.Read(dst[:5])
		for i := 0; i < n; i++ {
			if dst[i] != float32(want) {
				t.Fatalf("round %d: got %v, want %d", round, dst[i], want)
			}
			want++
		}
	}
	if want != 50 {
		t.Fatalf("read %d samples, want 50", want)
	}
}

func TestRingDropsNewestSamplesWhenFullAndCountsThem(t *testing.T) {
	r := NewRing(4)
	if got := r.Write(seq(0, 6)); got != 4 {
		t.Fatalf("Write = %d, want 4", got)
	}
	if got := r.Write(seq(10, 2)); got != 0 {
		t.Fatalf("Write on full ring = %d, want 0", got)
	}
	if d := r.Dropped(); d != 4 {
		t.Fatalf("Dropped = %d, want 4", d)
	}
	dst := make([]float32, 4)
	r.Read(dst)
	for i, v := range dst {
		if v != float32(i) {
			t.Fatalf("dst[%d] = %v, want %d (oldest samples must survive)", i, v, i)
		}
	}
}

func TestRingReadsPartiallyWhenFewerSamplesAreAvailable(t *testing.T) {
	r := NewRing(8)
	r.Write(seq(0, 3))
	dst := make([]float32, 6)
	if n := r.Read(dst); n != 3 {
		t.Fatalf("Read = %d, want 3", n)
	}
	if n := r.Read(dst); n != 0 {
		t.Fatalf("Read on empty ring = %d, want 0", n)
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
}

func TestRingKeepsOrderUnderConcurrentProducerAndConsumer(t *testing.T) {
	const total = 200000
	r := NewRing(256)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]float32, 37)
		sent := 0
		for sent < total {
			n := min(len(buf), total-sent)
			for i := 0; i < n; i++ {
				buf[i] = float32(sent + i)
			}
			// A full ring drops the tail of a write; only advance by what was
			// accepted so the stream the consumer sees stays gap-free.
			sent += r.Write(buf[:n])
		}
	}()
	dst := make([]float32, 64)
	got := 0
	for got < total {
		n := r.Read(dst)
		for i := 0; i < n; i++ {
			if dst[i] != float32(got) {
				t.Fatalf("sample %d = %v", got, dst[i])
			}
			got++
		}
	}
	wg.Wait()
}
