package live

// segment starts at a stream frame and says where the song is from there on.
// The stream clock (audioio.Stats.Frames) never stops; the song position does
// while paused. A timeline is a list of such segments, one appended at each
// Pause and each Resume.
type segment struct {
	// Stream is the first stream frame (device rate) of the segment.
	Stream int64 `json:"stream"`
	// Song is the song position in device-rate frames at Stream.
	Song int64 `json:"song"`
	// Paused means the source played silence and its position stayed at Song.
	Paused bool `json:"paused,omitempty"`
}

// timeline maps stream frames to song frames. It is not safe for concurrent
// use: the session guards it with its own mutex.
type timeline struct {
	segs []segment
}

func newTimeline() *timeline { return &timeline{segs: []segment{{}}} }

// timelineOf rebuilds a timeline from recorded segments.
func timelineOf(segs []segment) *timeline {
	if len(segs) == 0 {
		return newTimeline()
	}
	return &timeline{segs: append([]segment(nil), segs...)}
}

func (t *timeline) paused() bool { return t.segs[len(t.segs)-1].Paused }

// pause freezes the song at song frames from stream frame at on. It is a no-op
// when already paused.
func (t *timeline) pause(at, song int64) {
	if !t.paused() {
		t.segs = append(t.segs, segment{Stream: at, Song: song, Paused: true})
	}
}

// resume lets the song run again from stream frame at, from the position it
// froze at. It is a no-op when not paused.
func (t *timeline) resume(at int64) {
	if last := t.segs[len(t.segs)-1]; last.Paused {
		t.segs = append(t.segs, segment{Stream: at, Song: last.Song})
	}
}

// songFrame maps a (fractional) stream frame to the song position in frames.
// ok is false before the stream began and while the song was paused.
func (t *timeline) songFrame(stream float64) (song float64, ok bool) {
	if stream < 0 {
		return 0, false
	}
	for i := len(t.segs) - 1; i >= 0; i-- {
		s := t.segs[i]
		if float64(s.Stream) <= stream {
			if s.Paused {
				return 0, false
			}
			return float64(s.Song) + stream - float64(s.Stream), true
		}
	}
	return 0, false
}

func (t *timeline) segments() []segment { return append([]segment(nil), t.segs...) }
