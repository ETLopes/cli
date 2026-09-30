package calibrate

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/dsp"
)

// This file builds the synthetic world the calibration is tested in: a room is
// a known impulse response at the device rate, so the delay, the tail and the
// whole response the calibration recovers can be compared with the truth. The
// dsp test helpers live in another package's _test files and cannot be
// imported, so the room is rebuilt here.

// roomSpec describes one speaker-to-mic path.
type roomSpec struct {
	delay float64 // seconds before the direct sound arrives
	rt60  float64 // seconds for the tail to fall by 60 dB
	gain  float64 // amplitude of the direct sound
	seed  int64
}

type impulse struct{ t, a float64 }

// impulses is the room as a list of (time, amplitude) arrivals: the direct
// sound, eight early reflections within 25 ms, then a dense exponentially
// decaying scatter. Keeping the room as arrivals lets it be rendered at any
// sample rate, so the truth at 16 kHz does not go through a resampler.
func (s roomSpec) impulses() []impulse {
	rng := rand.New(rand.NewSource(s.seed))
	out := []impulse{{s.delay, s.gain}}
	for range 8 {
		out = append(out, impulse{s.delay + 0.0005 + 0.025*rng.Float64(), s.gain * (0.15 + 0.3*rng.Float64()) * float64(1-2*rng.Intn(2))})
	}
	for t := s.delay + 0.01; t < s.delay+1.2*s.rt60; t += 0.0002 {
		out = append(out, impulse{t, s.gain * 0.1 * math.Exp(-6.9078*(t-s.delay-0.01)/s.rt60) * rng.NormFloat64()})
	}
	return out
}

// render is the room's impulse response at the given rate, seconds long. Each
// arrival is a 5 kHz low-passed pulse (a Hann-windowed sinc), so the response
// is band-limited like a real speaker and mic. The pulse area is scaled by 2*cutoff/rate, which is what sampling a continuous response at that rate does, so the same room rendered at 16 kHz and at 48 kHz has the same frequency response.
func render(imps []impulse, rate int, seconds float64) []float64 {
	const cutoff, half = 5000.0, 0.0015
	ir := make([]float64, int(seconds*float64(rate)))
	for _, im := range imps {
		lo := int(math.Ceil((im.t - half) * float64(rate)))
		for n := max(lo, 0); n < len(ir) && float64(n)/float64(rate) <= im.t+half; n++ {
			d := float64(n)/float64(rate) - im.t
			k := 1.0
			if d != 0 {
				k = math.Sin(2*math.Pi*cutoff*d) / (2 * math.Pi * cutoff * d)
			}
			ir[n] += im.a * k * (2 * cutoff / float64(rate)) * (0.5 + 0.5*math.Cos(math.Pi*d/half))
		}
	}
	return ir
}

// convolver filters a stream block by block with overlap-add.
type convolver struct {
	fft   *dsp.RealFFT
	h     []complex128
	carry []float64
	in    []float64
	spec  []complex128
	out   []float64
}

func newConvolver(ir []float64, maxBlock int) *convolver {
	n := 4
	for n < maxBlock+len(ir) {
		n <<= 1
	}
	fft, err := dsp.NewRealFFT(n)
	if err != nil {
		panic(err)
	}
	c := &convolver{fft: fft, h: make([]complex128, fft.Bins()), carry: make([]float64, n),
		in: make([]float64, n), spec: make([]complex128, fft.Bins()), out: make([]float64, n)}
	buf := make([]float64, n)
	copy(buf, ir)
	fft.Forward(buf, c.h)
	return c
}

// process writes the filtered block into dst.
func (c *convolver) process(dst, src []float32) {
	clear(c.in)
	for i, v := range src {
		c.in[i] = float64(v)
	}
	c.fft.Forward(c.in, c.spec)
	for k := range c.spec {
		c.spec[k] *= c.h[k]
	}
	c.fft.Inverse(c.spec, c.out)
	for i := range c.out {
		c.out[i] += c.carry[i]
	}
	for i := range dst {
		dst[i] = float32(c.out[i])
	}
	n := len(src)
	copy(c.carry, c.out[n:])
	clear(c.carry[len(c.carry)-n:])
}

// micSpec is one scripted microphone.
type micSpec struct {
	room    *roomSpec // nil: nothing plugged in, only noise
	floor   float64   // noise floor, dBFS RMS
	overdrv float64   // extra gain on the echo, to force clipping; 0 means 1
}

// mic scripts what the microphone hears: the reference through the room, plus
// white noise.
func (m micSpec) capture(rate int, seed int64) audioio.CaptureFunc {
	rng := rand.New(rand.NewSource(seed))
	sigma := math.Pow(10, m.floor/20)
	var conv *convolver
	gain := 1.0
	if m.overdrv != 0 {
		gain = m.overdrv
	}
	if m.room != nil {
		conv = newConvolver(render(m.room.impulses(), rate, 1.19), 4096)
	}
	echo := make([]float32, 4096)
	return func(dst []float32, _ int64, ref []float32) {
		if conv != nil {
			conv.process(echo[:len(dst)], ref)
		}
		for i := range dst {
			dst[i] = float32(sigma * rng.NormFloat64())
			if conv != nil {
				dst[i] += float32(gain) * echo[i]
			}
		}
	}
}

func fakeInterface(rate int, mics ...micSpec) *audioio.Fake {
	f := &audioio.Fake{DeviceList: []audioio.Device{{
		ID: "fake-1", Name: "Fake Interface", CaptureChannels: 4, PlaybackChannels: 4, DefaultRate: rate, IsDefault: true,
	}}}
	for i, m := range mics {
		f.Script = append(f.Script, m.capture(rate, int64(100+i)))
	}
	return f
}

func testConfig(inputs ...int) Config {
	return Config{
		Stream: audioio.StreamConfig{DeviceName: "Fake Interface", Inputs: inputs, Outputs: [2]int{1, 2}},
		Pump: func(_ context.Context, s audioio.Stream, frames int) error {
			s.(*audioio.FakeStream).Step(frames)
			return nil
		},
	}
}

func correlation(a, b []float32) float64 {
	n := min(len(a), len(b))
	var ab, aa, bb float64
	for i := 0; i < n; i++ {
		ab += float64(a[i]) * float64(b[i])
		aa += float64(a[i]) * float64(a[i])
		bb += float64(b[i]) * float64(b[i])
	}
	return ab / math.Sqrt(aa*bb)
}

// checkAgainstTruth asserts the delay, tail and impulse response of a
// calibrated input against the room it was measured in, and returns the errors.
func checkAgainstTruth(t *testing.T, in Input, room roomSpec) (delayErrMs, tailErrPct, corr float64) {
	t.Helper()
	if in.NoEchoPath {
		t.Fatalf("input %d was flagged %q", in.Channel, FlagNoEchoPath)
	}
	truth := render(room.impulses(), dsp.PipelineRate, 1.19)
	truth32 := make([]float32, len(truth))
	for i, v := range truth {
		truth32[i] = float32(v)
	}
	delayErrMs = math.Abs(in.DelayMs - 1000*room.delay)
	_, wantTail := dsp.EchoTail(truth32, dsp.PipelineRate)
	wantTail = math.Min(math.Max(wantTail, 0.1), 0.5)
	tailErrPct = 100 * math.Abs(in.TailMs/1000-wantTail) / wantTail
	corr = correlation(in.IR, truth32[in.BulkDelay:])
	t.Logf("input %d: delay %.2f ms (true %.2f, error %.3f ms), tail %.0f ms (true %.0f, error %.1f%%), IR correlation %.4f, noise floor %.1f dBFS, residual floor %.1f dBFS, ERLE %.1f dB, %d taps, warnings %v",
		in.Channel, in.DelayMs, 1000*room.delay, delayErrMs, in.TailMs, 1000*wantTail, tailErrPct, corr,
		in.NoiseFloorDBFS, in.ResidualFloorDBFS, in.ERLEdB, len(in.IR), in.Warnings)
	if delayErrMs > 1 {
		t.Errorf("input %d: delay off by %.3f ms, want within 1", in.Channel, delayErrMs)
	}
	if tailErrPct > 20 {
		t.Errorf("input %d: tail %.0f ms is %.1f%% from the true %.0f ms, want within 20%%", in.Channel, in.TailMs, tailErrPct, 1000*wantTail)
	}
	if corr < 0.95 {
		t.Errorf("input %d: IR correlation %.4f, want at least 0.95", in.Channel, corr)
	}
	if in.ERLEdB < 20 {
		t.Errorf("input %d: seeded ERLE %.1f dB, want at least 20", in.Channel, in.ERLEdB)
	}
	return
}

var (
	roomA = roomSpec{delay: 0.0123, rt60: 0.35, gain: 0.5, seed: 1}
	roomB = roomSpec{delay: 0.0317, rt60: 0.25, gain: 0.35, seed: 2}
)

func TestCalibrationRecoversDelayTailAndResponseOfTwoMicsAt48kHz(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70}, micSpec{room: &roomB, floor: -70})
	res, err := Run(context.Background(), f, testConfig(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "Fake Interface" || res.SampleRate != 48000 || res.Outputs != [2]int{1, 2} || res.Time.IsZero() {
		t.Errorf("result header = %+v", res)
	}
	if len(res.Inputs) != 2 || res.Inputs[0].Channel != 1 || res.Inputs[1].Channel != 2 {
		t.Fatalf("inputs = %+v", res.Inputs)
	}
	checkAgainstTruth(t, res.Inputs[0], roomA)
	checkAgainstTruth(t, res.Inputs[1], roomB)
	if a, b := res.Inputs[0].NoiseFloorDBFS, -70.0; math.Abs(a-b) > 1.5 {
		t.Errorf("noise floor %.1f dBFS, want about %.0f", a, b)
	}
	if r := res.Inputs[0].ResidualFloorDBFS; r > -40 {
		t.Errorf("residual floor %.1f dBFS is not well below the echo", r)
	}
	if len(res.Usable()) != 2 {
		t.Errorf("Usable = %d inputs, want 2", len(res.Usable()))
	}
}

func TestCalibrationWorksOnA44100HzDevice(t *testing.T) {
	f := fakeInterface(44100, micSpec{room: &roomA, floor: -70})
	res, err := Run(context.Background(), f, testConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	if res.SampleRate != 44100 {
		t.Errorf("SampleRate = %d", res.SampleRate)
	}
	checkAgainstTruth(t, res.Inputs[0], roomA)
}

func TestCalibrationFlagsAMicWithNoEchoAndStillCalibratesTheOther(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70}, micSpec{floor: -60})
	res, err := Run(context.Background(), f, testConfig(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	checkAgainstTruth(t, res.Inputs[0], roomA)
	dead := res.Inputs[1]
	if !dead.NoEchoPath || len(dead.IR) != 0 {
		t.Errorf("silent mic: NoEchoPath %v with %d IR taps, want flagged and unseeded", dead.NoEchoPath, len(dead.IR))
	}
	if got := res.Usable(); len(got) != 1 || got[0].Channel != 1 {
		t.Errorf("Usable = %+v, want just input 1", got)
	}
	if _, err := dead.NewCanceller(); err == nil {
		t.Error("a canceller was built for a mic with no echo path")
	}
	if !strings.Contains(FlagNoEchoPath, "no echo path detected") {
		t.Errorf("flag text = %q", FlagNoEchoPath)
	}
}

func TestCalibrationRefusesAClippedSweepAndTellsTheUserToLowerTheLevel(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70, overdrv: 30})
	_, err := Run(context.Background(), f, testConfig(1))
	if !errors.Is(err, ErrClipping) {
		t.Fatalf("err = %v, want ErrClipping", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "lower the input gain") || !strings.Contains(msg, "speaker volume") || !strings.Contains(msg, "input 1") {
		t.Errorf("message %q does not say which input and what to do", msg)
	}
}

func TestCalibrationStopsWithTheContextError(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70})
	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(1)
	steps := 0
	inner := cfg.Pump
	cfg.Pump = func(ctx context.Context, s audioio.Stream, frames int) error {
		if steps++; steps == 30 {
			cancel()
		}
		return inner(ctx, s, frames)
	}
	_, err := Run(ctx, f, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if steps != 30 {
		t.Errorf("the loop ran %d steps after cancellation at 30", steps-30)
	}
}

func TestCalibrationReportsProgressStageByStage(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70})
	cfg := testConfig(1)
	var stages []Stage
	last := map[Stage]float64{}
	cfg.Progress = func(s Stage, frac float64) {
		if len(stages) == 0 || stages[len(stages)-1] != s {
			stages = append(stages, s)
		}
		if frac < 0 || frac > 1 {
			t.Errorf("progress %s %.3f is outside [0, 1]", s, frac)
		}
		last[s] = frac
	}
	if _, err := Run(context.Background(), f, cfg); err != nil {
		t.Fatal(err)
	}
	want := []Stage{StageLeadIn, StageSweep, StageTail, StageNoise, StageAnalysis}
	if len(stages) != len(want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages = %v, want %v", stages, want)
		}
	}
	if last[StageAnalysis] != 1 {
		t.Errorf("analysis ended at %.2f, want 1", last[StageAnalysis])
	}
}

func TestCalibrationPlaysTheSweepIdenticallyOnBothChannelsAtTheConfiguredLevel(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70})
	cfg := testConfig(1)
	cfg.Stream.Outputs = [2]int{3, 4}
	cfg.LevelDBFS = -12
	const rate = 48000
	sweepFrom, sweepTo := 1*rate, 5*rate // default lead-in and sweep
	noiseFrom, noiseTo := 6200*rate/1000, 11200*rate/1000
	var sweepPeak, noisePow float64
	var differ bool
	frame := 0
	cfg.Pump = func(ctx context.Context, s audioio.Stream, frames int) error {
		out := s.(*audioio.FakeStream).Step(frames)
		for i := 0; i+3 < len(out); i, frame = i+4, frame+1 {
			v := float64(out[i+2])
			differ = differ || out[i+2] != out[i+3] || out[i] != 0 || out[i+1] != 0
			if frame >= sweepFrom && frame < sweepTo {
				sweepPeak = math.Max(sweepPeak, math.Abs(v))
			}
			if frame >= noiseFrom && frame < noiseTo {
				noisePow += v * v
			}
		}
		return nil
	}
	res, err := Run(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if differ {
		t.Error("the two channels of the pair differ, or another output channel is not silent")
	}
	want := math.Pow(10, -12.0/20)
	if math.Abs(sweepPeak-want) > 0.02*want {
		t.Errorf("sweep peak %.4f, want %.4f (-12 dBFS)", sweepPeak, want)
	}
	if noiseRMS := math.Sqrt(noisePow / float64(noiseTo-noiseFrom)); math.Abs(noiseRMS-want) > 0.1*want {
		t.Errorf("noise RMS %.4f, want about %.4f (-12 dBFS)", noiseRMS, want)
	}
	if res.Outputs != [2]int{3, 4} {
		t.Errorf("Outputs = %v", res.Outputs)
	}
	checkAgainstTruth(t, res.Inputs[0], roomA)
}

// music is a tonal backing track stand-in at 16 kHz: chords that change every
// 0.6 s over a soft noise bed. Tonal material is what makes an unseeded
// canceller slow.
func music(n int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	chords := [][]float64{{130.8, 164.8, 196}, {110, 138.6, 164.8}, {146.8, 174.6, 220}}
	for pos := 0; pos < n; pos += 9600 {
		ch := chords[rng.Intn(len(chords))]
		for i := 0; i < 9600 && pos+i < n; i++ {
			tt := float64(pos+i) / dsp.PipelineRate
			var v float64
			for _, f := range ch {
				for h := 1.0; h <= 6; h++ {
					v += math.Sin(2*math.Pi*f*h*tt) / h
				}
			}
			out[pos+i] = float32(0.05*v + 0.02*rng.NormFloat64())
		}
	}
	return out
}

func TestCancellerSeededFromTheStoredCalibrationConvergesFasterThanUnseeded(t *testing.T) {
	f := fakeInterface(48000, micSpec{room: &roomA, floor: -70})
	res, err := Run(context.Background(), f, testConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Save(dir, res); err != nil {
		t.Fatal(err)
	}
	stored, found, err := Load(dir, res.Key())
	if err != nil || !found {
		t.Fatalf("Load: found %v, err %v", found, err)
	}
	in := stored.Inputs[0]

	// The same room at 16 kHz, rendered from its arrivals, not from the
	// calibration, so the test does not grade the calibration against itself.
	const secs = 3
	ref := music(secs*dsp.PipelineRate, 7)
	mic := make([]float32, len(ref))
	newConvolver(render(roomA.impulses(), dsp.PipelineRate, 0.6), len(ref)).process(mic, ref)

	seeded, err := in.NewCanceller()
	if err != nil {
		t.Fatal(err)
	}
	unseeded, err := dsp.NewCanceller(dsp.CancellerConfig{Tail: len(in.IR), BulkDelay: in.BulkDelay})
	if err != nil {
		t.Fatal(err)
	}
	erle := func(c *dsp.Canceller) float64 {
		var micPow, outPow float64
		out := make([]float32, dsp.CancellerBlock)
		for p := 0; p+dsp.CancellerBlock <= len(ref); p += dsp.CancellerBlock {
			c.Process(ref[p:p+dsp.CancellerBlock], mic[p:p+dsp.CancellerBlock], out, nil)
			if p >= dsp.PipelineRate {
				continue // the first second is the one that matters
			}
			for i, v := range out {
				micPow += float64(mic[p+i]) * float64(mic[p+i])
				outPow += float64(v) * float64(v)
			}
		}
		return 10 * math.Log10(micPow/outPow)
	}
	s, u := erle(seeded), erle(unseeded)
	t.Logf("ERLE over the first second: seeded %.1f dB, unseeded %.1f dB", s, u)
	if s < u+10 {
		t.Errorf("seeded ERLE %.1f dB is not 10 dB above unseeded %.1f dB", s, u)
	}
}
