package audioio

// maxChunkFrames bounds the scratch buffers. A fixed-size device callback is one
// period (a few hundred frames), so this is never reached in practice; a larger
// callback is processed in chunks rather than allocating on the audio thread.
const maxChunkFrames = 4096

// engine is the body of the audio callback, shared by the malgo backend and the
// fake so that both run the same code path. Everything it needs is allocated in
// newEngine; playback and capture only copy.
//
// Alignment guarantee: every call advances the reference ring and every capture
// ring by the same number of frames, and Stats.Frames counts them once. Sample n
// of each capture ring therefore corresponds to sample n of the reference ring,
// as long as no ring has overflowed (the overflow counters say when one has).
type engine struct {
	inputs  []int
	pair    [2]int
	inCh    int // interleaved width of the device capture buffer
	outCh   int // interleaved width of the device playback buffer
	source  PlaybackSource
	fin     Finisher
	capture []*Ring
	ref     *Ring
	stats   Stats

	stereo     []float32   // playback pulled from the source
	mono       []float32   // reference downmix of the chunk
	capScratch [][]float32 // one deinterleaved chunk per input
}

// newEngine builds the callback state for a validated config at the given rate.
func newEngine(cfg StreamConfig, rate int) *engine {
	e := &engine{
		inputs:     append([]int(nil), cfg.Inputs...),
		pair:       cfg.Outputs,
		source:     cfg.Source,
		stereo:     make([]float32, 2*maxChunkFrames),
		mono:       make([]float32, maxChunkFrames),
		ref:        NewRing(ringSeconds * rate),
		capture:    make([]*Ring, len(cfg.Inputs)),
		capScratch: make([][]float32, len(cfg.Inputs)),
	}
	e.fin, _ = cfg.Source.(Finisher)
	for i, in := range e.inputs {
		e.inCh = max(e.inCh, in)
		e.capture[i] = NewRing(ringSeconds * rate)
		e.capScratch[i] = make([]float32, maxChunkFrames)
	}
	e.outCh = max(e.pair[0], e.pair[1])
	return e
}

// process handles one device callback: out holds frames*outCh samples to fill,
// in holds frames*inCh captured samples (nil is treated as silence).
func (e *engine) process(out, in []float32, frames int) {
	for off := 0; off < frames; off += maxChunkFrames {
		n := min(maxChunkFrames, frames-off)
		e.playback(out[off*e.outCh:], n)
		var chunk []float32
		if len(in) >= (off+n)*e.inCh {
			chunk = in[off*e.inCh:]
		}
		e.deliver(chunk, n)
	}
}

// playback fills out (n*outCh samples) from the source, records the mono
// downmix in the reference ring and returns that downmix. n <= maxChunkFrames.
// The returned slice is only valid until the next call.
func (e *engine) playback(out []float32, n int) []float32 {
	stereo := e.stereo[:2*n]
	got := 0
	if e.source != nil {
		got = min(e.source.Read(stereo), n)
	}
	clear(stereo[2*got:])
	if got < n && e.fin != nil && !e.fin.Done() {
		e.stats.SourceUnderruns.Add(uint64(n - got))
	}
	InterleavePair(out, e.outCh, e.pair, stereo, n)
	Downmix(e.mono, stereo, n)
	if w := e.ref.Write(e.mono[:n]); w < n {
		e.stats.ReferenceOverflows.Add(uint64(n - w))
	}
	e.stats.Frames.Add(uint64(n))
	return e.mono[:n]
}

// deliver pushes n captured frames into the capture rings. A nil in delivers
// silence so the rings stay aligned with the reference.
func (e *engine) deliver(in []float32, n int) {
	for i := range e.capture {
		scratch := e.capScratch[i][:n]
		if in == nil {
			clear(scratch)
		} else {
			Deinterleave(e.capScratch[i:i+1], in, e.inCh, e.inputs[i:i+1], n)
		}
		if w := e.capture[i].Write(scratch); w < n {
			e.stats.CaptureOverflows.Add(uint64(n - w))
		}
	}
}
