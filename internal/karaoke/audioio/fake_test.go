package audioio

import (
	"errors"
	"math"
	"strings"
	"testing"
)

var scarlett = Device{
	ID: "scarlett", Name: "Scarlett 18i20", CaptureChannels: 18, PlaybackChannels: 20,
	DefaultRate: 48000, IsDefault: true,
}

// testTrack returns n stereo frames with distinct, non-trivial samples.
func testTrack(n int) []float32 {
	s := make([]float32, 2*n)
	for i := 0; i < n; i++ {
		s[2*i] = float32(math.Sin(float64(i) * 0.01))
		s[2*i+1] = float32(math.Cos(float64(i) * 0.013))
	}
	return s
}

func openFake(t *testing.T, f *Fake, cfg StreamConfig) *FakeStream {
	t.Helper()
	if f.DeviceList == nil {
		f.DeviceList = []Device{scarlett}
	}
	s, err := f.OpenFake(cfg)
	if err != nil {
		t.Fatalf("OpenFake: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s
}

func drain(r *Ring) []float32 {
	out := make([]float32, r.Len())
	r.Read(out)
	return out
}

func TestFakeReferenceIsTheMonoDownmixOfTheOutputPair(t *testing.T) {
	track := testTrack(1000)
	s := openFake(t, &Fake{}, StreamConfig{
		Inputs: []int{1}, Outputs: [2]int{3, 4}, Source: NewBufferSource(track),
	})
	out := s.Step(1000)
	ref := drain(s.Reference())
	if len(ref) != 1000 {
		t.Fatalf("reference has %d samples, want 1000", len(ref))
	}
	for f := 0; f < 1000; f++ {
		l, r := out[f*4+2], out[f*4+3]
		if l != track[2*f] || r != track[2*f+1] {
			t.Fatalf("frame %d: output pair (%v, %v) is not the source (%v, %v)", f, l, r, track[2*f], track[2*f+1])
		}
		if want := (l + r) / 2; ref[f] != want {
			t.Fatalf("frame %d: reference %v, want downmix %v", f, ref[f], want)
		}
	}
}

func TestFakeReferenceIsSilentWhilePausedAndAfterTheSourceEnds(t *testing.T) {
	src := NewBufferSource(testTrack(300))
	s := openFake(t, &Fake{}, StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}, Source: src})

	s.Step(100)
	src.Pause()
	s.Step(100)
	src.Resume()
	s.Step(300) // 200 frames of track left, then 100 frames past the end
	ref := drain(s.Reference())
	if len(ref) != 500 {
		t.Fatalf("reference has %d samples, want 500", len(ref))
	}
	track := testTrack(300)
	for f := 0; f < 500; f++ {
		var want float32
		switch {
		case f < 100:
			want = (track[2*f] + track[2*f+1]) / 2
		case f < 200: // paused
			want = 0
		// f >= 400: the source has ended, so silence again
		case f < 400:
			g := f - 100
			want = (track[2*g] + track[2*g+1]) / 2
		}
		if ref[f] != want {
			t.Fatalf("reference[%d] = %v, want %v", f, ref[f], want)
		}
	}
	if !src.Done() {
		t.Fatal("source should be Done after the track ended")
	}
	if u := s.Stats().SourceUnderruns.Load(); u != 0 {
		t.Fatalf("end of track counted as %d underruns", u)
	}
}

func TestFakeCaptureIsAlignedSampleExactlyWithTheReference(t *testing.T) {
	const delay = 37
	var hist [delay]float32
	echo := func(dst []float32, first int64, ref []float32) {
		for i, v := range ref {
			slot := int(first+int64(i)) % delay
			dst[i] = hist[slot]
			hist[slot] = v
		}
	}
	// 10000 frames spans several internal chunks.
	s := openFake(t, &Fake{Script: []CaptureFunc{echo}}, StreamConfig{
		Inputs: []int{2}, Outputs: [2]int{1, 2}, Source: NewBufferSource(testTrack(10000)),
	})
	s.Step(10000)
	ref, mic := drain(s.Reference()), drain(s.Capture(0))
	if len(ref) != 10000 || len(mic) != 10000 {
		t.Fatalf("got %d reference and %d capture samples, want 10000 each", len(ref), len(mic))
	}
	for n := delay; n < len(mic); n++ {
		if mic[n] != ref[n-delay] {
			t.Fatalf("capture[%d] = %v, want reference[%d] = %v", n, mic[n], n-delay, ref[n-delay])
		}
	}
	for n := 0; n < delay; n++ {
		if mic[n] != 0 {
			t.Fatalf("capture[%d] = %v before the echo arrives, want 0", n, mic[n])
		}
	}
}

func TestFakeCaptureFollowsConfigOrderAndSeparatesInputs(t *testing.T) {
	tone := func(v float32) CaptureFunc {
		return func(dst []float32, _ int64, _ []float32) {
			for i := range dst {
				dst[i] = v
			}
		}
	}
	s := openFake(t, &Fake{Script: []CaptureFunc{tone(0.5), tone(-0.25)}}, StreamConfig{
		Inputs: []int{5, 1}, Outputs: [2]int{1, 2},
	})
	s.Step(64)
	if got := drain(s.Capture(0)); got[10] != 0.5 {
		t.Fatalf("Capture(0) = %v, want 0.5", got[10])
	}
	if got := drain(s.Capture(1)); got[10] != -0.25 {
		t.Fatalf("Capture(1) = %v, want -0.25", got[10])
	}
}

func TestFakeSilencesEveryOutputChannelOutsideThePair(t *testing.T) {
	s := openFake(t, &Fake{}, StreamConfig{
		Inputs: []int{1}, Outputs: [2]int{19, 20}, Source: NewBufferSource(testTrack(50)),
	})
	out := s.Step(50)
	for f := 0; f < 50; f++ {
		for c := 0; c < 18; c++ {
			if out[f*20+c] != 0 {
				t.Fatalf("frame %d channel %d = %v, want silence", f, c+1, out[f*20+c])
			}
		}
	}
}

func TestFakeReportsRateAndPeriod(t *testing.T) {
	s := openFake(t, &Fake{}, StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}})
	if s.SampleRate() != 48000 || s.PeriodFrames() != 480 {
		t.Fatalf("defaults: rate %d period %d, want 48000 and 480", s.SampleRate(), s.PeriodFrames())
	}
	s = openFake(t, &Fake{}, StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}, SampleRate: 44100, PeriodFrames: 256})
	if s.SampleRate() != 44100 || s.PeriodFrames() != 256 {
		t.Fatalf("explicit: rate %d period %d", s.SampleRate(), s.PeriodFrames())
	}
}

func TestFakeStepDoesNothingUntilStartedOrAfterStop(t *testing.T) {
	f := &Fake{DeviceList: []Device{scarlett}}
	s, err := f.OpenFake(StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Step(10) != nil || s.Stats().Frames.Load() != 0 {
		t.Fatal("Step ran on a stream that was never started")
	}
	s.Start()
	s.Step(10)
	s.Stop()
	if s.Step(10) != nil || s.Stats().Frames.Load() != 10 {
		t.Fatalf("Step ran on a stopped stream; frames = %d", s.Stats().Frames.Load())
	}
}

func TestFakeCountsRingOverflowsWithoutBlocking(t *testing.T) {
	s := openFake(t, &Fake{}, StreamConfig{Inputs: []int{1, 2}, Outputs: [2]int{1, 2}})
	capacity := s.Reference().Cap()
	extra := 500
	s.Step(capacity + extra)
	st := s.Stats()
	if st.Frames.Load() != uint64(capacity+extra) {
		t.Fatalf("Frames = %d", st.Frames.Load())
	}
	if got := st.ReferenceOverflows.Load(); got != uint64(extra) {
		t.Fatalf("ReferenceOverflows = %d, want %d", got, extra)
	}
	if got := st.CaptureOverflows.Load(); got != uint64(2*extra) {
		t.Fatalf("CaptureOverflows = %d, want %d (two inputs)", got, 2*extra)
	}
}

// streamingSource returns short reads without being finished, like a live feed
// that could not keep up.
type streamingSource struct{}

func (streamingSource) Read(dst []float32) int { return len(dst) / 4 }
func (streamingSource) Done() bool             { return false }

func TestFakeCountsUnderrunsWhenARunningSourceFallsShort(t *testing.T) {
	s := openFake(t, &Fake{}, StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}, Source: streamingSource{}})
	s.Step(400)
	if got := s.Stats().SourceUnderruns.Load(); got != 200 {
		t.Fatalf("SourceUnderruns = %d, want 200", got)
	}
}

func TestFakeOpenRejectsChannelsBeyondTheDevice(t *testing.T) {
	f := &Fake{DeviceList: []Device{scarlett}}
	_, err := f.Open(StreamConfig{Inputs: []int{1, 19}, Outputs: [2]int{1, 2}})
	if err == nil || !strings.Contains(err.Error(), "18") {
		t.Fatalf("input 19 on 18 channels: err = %v, want a message naming 18", err)
	}
	_, err = f.Open(StreamConfig{Inputs: []int{1}, Outputs: [2]int{19, 21}})
	if err == nil || !strings.Contains(err.Error(), "20") {
		t.Fatalf("output 21 on 20 channels: err = %v, want a message naming 20", err)
	}
}

func TestValidateRejectsMalformedSelections(t *testing.T) {
	cases := map[string]StreamConfig{
		"no inputs":       {Outputs: [2]int{1, 2}},
		"input zero":      {Inputs: []int{0}, Outputs: [2]int{1, 2}},
		"duplicate input": {Inputs: []int{2, 2}, Outputs: [2]int{1, 2}},
		"output zero":     {Inputs: []int{1}, Outputs: [2]int{0, 2}},
		"same output":     {Inputs: []int{1}, Outputs: [2]int{3, 3}},
	}
	for name, cfg := range cases {
		if err := Validate(cfg, scarlett); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, cfg)
		}
	}
	if err := Validate(StreamConfig{Inputs: []int{18}, Outputs: [2]int{19, 20}}, scarlett); err != nil {
		t.Errorf("highest valid channels rejected: %v", err)
	}
}

func TestFakeOpenFindsTheDeviceByIDThenName(t *testing.T) {
	other := Device{ID: "other", Name: "Other", CaptureChannels: 2, PlaybackChannels: 2, DefaultRate: 44100}
	f := &Fake{DeviceList: []Device{scarlett, other}}
	s, err := f.Open(StreamConfig{DeviceName: "other", Inputs: []int{1}, Outputs: [2]int{1, 2}})
	if err != nil || s.SampleRate() != 44100 {
		t.Fatalf("by name: %v rate %v", err, s)
	}
	s, err = f.Open(StreamConfig{DeviceID: "scarlett", DeviceName: "other", Inputs: []int{1}, Outputs: [2]int{1, 2}})
	if err != nil || s.SampleRate() != 48000 {
		t.Fatalf("ID must win over name: %v", err)
	}
	if _, err = f.Open(StreamConfig{DeviceName: "nope", Inputs: []int{1}, Outputs: [2]int{1, 2}}); err == nil {
		t.Fatal("unknown device accepted")
	}
	if f.Last() == nil {
		t.Fatal("Last is nil after Open")
	}
	var _ Backend = f
	var _ Stream = s
	if errors.Is(err, ErrUnsupported) {
		t.Fatal("fake must not report ErrUnsupported")
	}
}

func TestFakeStepAllocatesNothingAfterWarmUp(t *testing.T) {
	echo := func(dst []float32, _ int64, ref []float32) { copy(dst, ref) }
	s := openFake(t, &Fake{Script: []CaptureFunc{echo, echo}}, StreamConfig{
		Inputs: []int{1, 5}, Outputs: [2]int{1, 2}, Source: NewBufferSource(testTrack(1 << 20)),
	})
	buf := make([]float32, 480)
	s.Step(480)
	allocs := testing.AllocsPerRun(200, func() {
		s.Step(480)
		s.Reference().Read(buf)
		s.Capture(0).Read(buf)
		s.Capture(1).Read(buf)
	})
	if allocs != 0 {
		t.Fatalf("Step allocated %v times per run, want 0", allocs)
	}
}
