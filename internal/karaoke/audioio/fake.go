package audioio

import (
	"errors"
	"fmt"
)

// CaptureFunc scripts what one microphone hears. It fills dst with the mono
// samples for stream frames [firstFrame, firstFrame+len(dst)), given ref, the
// mono reference (what the speakers played) for the same frames. A room is a
// closure over this: echo is ref convolved with an impulse response, plus a
// singer. It runs inside Step, so a test that checks allocations must not
// allocate in it.
type CaptureFunc func(dst []float32, firstFrame int64, ref []float32)

// Fake is a deterministic Backend for tests. It has no goroutines and no clock:
// the test drives the stream by calling FakeStream.Step, which does exactly what
// the real callback does for that many frames.
type Fake struct {
	// DeviceList is what Devices returns.
	DeviceList []Device
	// Script[i] scripts the capture for StreamConfig.Inputs[i]. A missing or nil
	// entry captures silence.
	Script []CaptureFunc

	last *FakeStream
}

// Devices implements Backend.
func (f *Fake) Devices() ([]Device, error) {
	return append([]Device(nil), f.DeviceList...), nil
}

// Open implements Backend.
func (f *Fake) Open(cfg StreamConfig) (Stream, error) {
	s, err := f.OpenFake(cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Last returns the stream most recently opened, for tests that opened it through
// the Backend interface.
func (f *Fake) Last() *FakeStream { return f.last }

// OpenFake is Open with the concrete type, so a test can call Step.
func (f *Fake) OpenFake(cfg StreamConfig) (*FakeStream, error) {
	dev, err := f.pick(cfg)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg, dev); err != nil {
		return nil, fmt.Errorf("open fake stream: %w", err)
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = dev.DefaultRate
	}
	if rate == 0 {
		rate = 48000
	}
	period := cfg.PeriodFrames
	if period == 0 {
		period = rate / 100
	}
	s := &FakeStream{
		e:      newEngine(cfg, rate),
		rate:   rate,
		period: period,
		script: append([]CaptureFunc(nil), f.Script...),
	}
	s.mic = make([]float32, maxChunkFrames)
	f.last = s
	return s, nil
}

func (f *Fake) pick(cfg StreamConfig) (Device, error) {
	if len(f.DeviceList) == 0 {
		return Device{}, errors.New("fake backend has no devices")
	}
	if cfg.DeviceID == "" && cfg.DeviceName == "" {
		for _, d := range f.DeviceList {
			if d.IsDefault {
				return d, nil
			}
		}
		return f.DeviceList[0], nil
	}
	return findDevice(f.DeviceList, cfg)
}

// FakeStream is a Stream driven by Step.
type FakeStream struct {
	e       *engine
	rate    int
	period  int
	script  []CaptureFunc
	started bool
	closed  bool

	in  []float32 // interleaved device capture buffer, grown on demand
	out []float32 // interleaved device playback buffer, grown on demand
	mic []float32 // one scripted microphone chunk
}

// Start implements Stream.
func (s *FakeStream) Start() error {
	if s.closed {
		return errors.New("fake stream is closed")
	}
	s.started = true
	return nil
}

// Stop implements Stream.
func (s *FakeStream) Stop() error { s.started = false; return nil }

// Close implements Stream.
func (s *FakeStream) Close() error { s.started = false; s.closed = true; return nil }

// SampleRate implements Stream.
func (s *FakeStream) SampleRate() int { return s.rate }

// PeriodFrames implements Stream.
func (s *FakeStream) PeriodFrames() int { return s.period }

// Capture implements Stream.
func (s *FakeStream) Capture(i int) *Ring { return s.e.capture[i] }

// Reference implements Stream.
func (s *FakeStream) Reference() *Ring { return s.e.ref }

// Stats implements Stream.
func (s *FakeStream) Stats() *Stats { return &s.e.stats }

// Step runs the callback for exactly frames frames and returns the interleaved
// device output it produced (valid until the next Step), so a test can inspect
// every output channel. Like a stopped device, a stream that is not started does
// nothing and returns nil.
//
// The first Step of a given size allocates the device buffers; later Steps of
// that size or smaller are allocation-free, like the real callback.
func (s *FakeStream) Step(frames int) []float32 {
	if !s.started || frames <= 0 {
		return nil
	}
	e := s.e
	if need := frames * e.outCh; len(s.out) < need {
		s.out = make([]float32, need)
	}
	if need := frames * e.inCh; len(s.in) < need {
		s.in = make([]float32, need)
	}
	for off := 0; off < frames; off += maxChunkFrames {
		n := min(maxChunkFrames, frames-off)
		first := int64(e.stats.Frames.Load())
		ref := e.playback(s.out[off*e.outCh:], n)

		in := s.in[off*e.inCh : (off+n)*e.inCh]
		clear(in)
		for i, ch := range e.inputs {
			mic := s.mic[:n]
			clear(mic)
			if i < len(s.script) && s.script[i] != nil {
				s.script[i](mic, first, ref)
			}
			for f, v := range mic {
				in[f*e.inCh+ch-1] = v
			}
		}
		e.deliver(in, n)
	}
	return s.out[:frames*e.outCh]
}
