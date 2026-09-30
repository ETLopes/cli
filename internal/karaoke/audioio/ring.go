package audioio

import "sync/atomic"

// Ring is a lock-free single-producer single-consumer ring of float32 samples.
//
// The audio callback is the producer and must never wait on the consumer, so a
// full ring drops the newest samples (and counts them) rather than blocking or
// overwriting. Dropping the newest keeps the data the consumer is about to read
// contiguous; the count tells it that the stream now has a gap.
//
// Exactly one goroutine may call Write and exactly one may call Read.
type Ring struct {
	buf     []float32
	mask    uint64
	head    atomic.Uint64 // next index to read; advanced only by the consumer
	tail    atomic.Uint64 // next index to write; advanced only by the producer
	dropped atomic.Uint64
}

// NewRing returns a ring holding at least capacity samples. The capacity is
// rounded up to a power of two so indexing is a mask instead of a modulo.
func NewRing(capacity int) *Ring {
	n := 1
	for n < capacity {
		n <<= 1
	}
	return &Ring{buf: make([]float32, n), mask: uint64(n - 1)}
}

// Cap is the number of samples the ring can hold.
func (r *Ring) Cap() int { return len(r.buf) }

// Len is the number of samples currently readable.
func (r *Ring) Len() int { return int(r.tail.Load() - r.head.Load()) }

// Dropped is the total number of samples Write discarded because the ring was full.
func (r *Ring) Dropped() uint64 { return r.dropped.Load() }

// Write appends as many samples of src as fit and returns how many were
// accepted. The rest are dropped and counted. Producer side only.
func (r *Ring) Write(src []float32) int {
	tail := r.tail.Load()
	free := uint64(len(r.buf)) - (tail - r.head.Load())
	n := uint64(len(src))
	if n > free {
		r.dropped.Add(n - free)
		n = free
	}
	if n == 0 {
		return 0
	}
	start := tail & r.mask
	first := copy(r.buf[start:], src[:n])
	copy(r.buf, src[first:n])
	r.tail.Store(tail + n)
	return int(n)
}

// Read copies up to len(dst) samples into dst and returns how many it copied.
// Consumer side only.
func (r *Ring) Read(dst []float32) int {
	head := r.head.Load()
	n := min(uint64(len(dst)), r.tail.Load()-head)
	if n == 0 {
		return 0
	}
	start := head & r.mask
	first := copy(dst[:n], r.buf[start:])
	copy(dst[first:n], r.buf)
	r.head.Store(head + n)
	return int(n)
}
