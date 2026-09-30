package audioio

import "sync/atomic"

// PlaybackSource supplies the instrumental to the audio callback.
//
// Read runs on the real-time thread, so implementations must not block,
// allocate or take locks.
type PlaybackSource interface {
	// Read fills dst with interleaved stereo frames at the stream rate and
	// returns the number of frames written. Fewer frames than len(dst)/2 means
	// the track ended; the callback pads the rest with silence.
	Read(dst []float32) int
}

// Finisher is optionally implemented by a source to say whether it has really
// ended. Without it, a short read is simply the end of the track. With it, a
// short read from a source that is not Done counts as an underrun.
type Finisher interface {
	Done() bool
}

// BufferSource plays a preloaded interleaved stereo buffer. Resampling to the
// device rate is the caller's job: doing it here would cost time in the callback.
//
// Read is called from the audio thread; Pause, Resume, SeekTo and Position may be
// called from any goroutine.
type BufferSource struct {
	samples []float32 // interleaved stereo
	frames  int64
	pos     atomic.Int64
	paused  atomic.Bool
}

// NewBufferSource wraps interleaved stereo samples (L, R, L, R, ...). A trailing
// odd sample is ignored.
func NewBufferSource(samples []float32) *BufferSource {
	return &BufferSource{samples: samples, frames: int64(len(samples) / 2)}
}

// Read implements PlaybackSource. While paused it returns full frames of silence
// and does not advance: the stream clock and the capture keep running, and the
// reference ring records the silence, but the track resumes where it stopped.
func (s *BufferSource) Read(dst []float32) int {
	want := int64(len(dst) / 2)
	if s.paused.Load() {
		clear(dst[:want*2])
		return int(want)
	}
	pos := s.pos.Load()
	n := min(want, s.frames-pos)
	if n <= 0 {
		return 0
	}
	copy(dst, s.samples[pos*2:(pos+n)*2])
	// If SeekTo moved the position while we copied, the seek wins.
	s.pos.CompareAndSwap(pos, pos+n)
	return int(n)
}

// Pause makes the source play silence until Resume.
func (s *BufferSource) Pause() { s.paused.Store(true) }

// Resume continues playback from where Pause stopped it.
func (s *BufferSource) Resume() { s.paused.Store(false) }

// Paused reports whether the source is paused.
func (s *BufferSource) Paused() bool { return s.paused.Load() }

// SeekTo moves the playback position to the given frame, clamped to the track.
func (s *BufferSource) SeekTo(frame int64) {
	s.pos.Store(min(max(frame, 0), s.frames))
}

// Position is the next frame that will be played.
func (s *BufferSource) Position() int64 { return s.pos.Load() }

// Len is the track length in frames.
func (s *BufferSource) Len() int64 { return s.frames }

// Done reports whether the whole track has been played.
func (s *BufferSource) Done() bool { return s.pos.Load() >= s.frames }
