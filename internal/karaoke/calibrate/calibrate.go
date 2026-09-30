// Package calibrate measures the echo path from the karaoke speakers to each
// microphone and persists it per device, sample rate and output pair.
//
// The measurement is one duplex stream: a lead-in of silence (the mic noise
// floor), an exponential sweep (the impulse response, hence the echo tail),
// a stretch of silence (so the tail is recorded), and pink noise (a stand-in
// for music, used to measure how well a canceller seeded from the impulse
// response does). Everything is played identically on both channels of the
// output pair, because karaoke plays mono through both speakers and the mic
// hears their sum as one echo path.
package calibrate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/dsp"
)

// Stage names the part of the calibration in progress, for the UI.
type Stage string

const (
	StageLeadIn   Stage = "lead-in"
	StageSweep    Stage = "sweep"
	StageTail     Stage = "tail"
	StageNoise    Stage = "noise"
	StageAnalysis Stage = "analysis"
)

// ErrClipping is wrapped by the error Run returns when a microphone clipped
// during the sweep. A clipped sweep is nonlinear and the impulse response
// measured from it would be wrong, so the calibration is refused.
var ErrClipping = errors.New("input clipped during the sweep")

// FlagNoEchoPath is the human-readable form of Input.NoEchoPath.
const FlagNoEchoPath = "no echo path detected"

// Config configures Run. The zero value of every field but Stream selects a
// sensible default.
type Config struct {
	// Stream selects the device, the mics (Inputs) and the output pair. Source
	// is ignored: calibration supplies its own playback. SampleRate 0 keeps the
	// device's current rate.
	Stream audioio.StreamConfig
	// LevelDBFS is the level of the sweep (its peak) and of the noise (its
	// RMS). 0 means -18: full scale is never a sensible calibration level.
	LevelDBFS float64
	// LeadIn, SweepDuration, TailSilence and NoiseDuration default to 1 s, 4 s,
	// 1.2 s and 5 s. The impulse response can be at most TailSilence long,
	// counted from the start of the sweep.
	LeadIn, SweepDuration, TailSilence, NoiseDuration time.Duration
	// Progress, if not nil, is called after every step with the stage in
	// progress and the fraction of it done.
	Progress func(stage Stage, fraction float64)
	// Pump advances the stream by frames frames of device time and returns when
	// they have elapsed. The default sleeps for that long, because a real device
	// advances itself on its own thread; a test using audioio.Fake supplies a
	// pump that calls FakeStream.Step, so the same loop runs in both worlds.
	Pump func(ctx context.Context, s audioio.Stream, frames int) error
	// Now stamps the result; nil means time.Now.
	Now func() time.Time
}

const (
	defaultLevelDBFS = -18.0
	sweepLowHz       = 50.0
	sweepHighHz      = 7500.0
	stepMillis       = 20 // device time advanced per loop iteration
	clipLevel        = 0.99
	// stallLimit is how long the rings may stay empty before the stream is
	// declared dead.
	stallLimit = 5 * time.Second

	// A direct-path peak must stand this many robust sigmas above the noise of
	// the impulse response. Pure noise deconvolves to a peak of about 4-5 sigma
	// over a second of samples; a real echo is far above 10.
	minPeakToNoise = 10.0
	// preRoll is how far before the measured delay the canceller's delay is
	// set, so the band-limited pre-ringing of the direct sound is inside the
	// filter (samples at 16 kHz).
	preRoll = 8
	// delayDisagreeSamples is 2 ms at 16 kHz.
	delayDisagreeSamples = 32
	minTail              = 100 * time.Millisecond
	maxTail              = 500 * time.Millisecond
	// settle is how much of the noise segment is skipped before measuring the
	// residual: the resampler and the canceller's first blocks are transients.
	settle = time.Second
)

func (c *Config) defaults() {
	if c.LevelDBFS == 0 {
		c.LevelDBFS = defaultLevelDBFS
	}
	if c.LeadIn == 0 {
		c.LeadIn = time.Second
	}
	if c.SweepDuration == 0 {
		c.SweepDuration = 4 * time.Second
	}
	if c.TailSilence == 0 {
		c.TailSilence = 1200 * time.Millisecond
	}
	if c.NoiseDuration == 0 {
		c.NoiseDuration = 5 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Progress == nil {
		c.Progress = func(Stage, float64) {}
	}
	if c.Pump == nil {
		c.Pump = sleepPump
	}
}

func sleepPump(ctx context.Context, s audioio.Stream, frames int) error {
	timer := time.NewTimer(time.Duration(frames) * time.Second / time.Duration(s.SampleRate()))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// layout is the playback timeline in frames at the device rate.
type layout struct {
	leadIn, sweep, tail, noise int
}

func (l layout) sweepStart() int { return l.leadIn }
func (l layout) noiseStart() int { return l.leadIn + l.sweep + l.tail }

// total includes a quarter second of silence after the noise: the echo of its
// last samples reaches the mic that much later, and the rings are only
// complete once it has.
func (l layout) total(rate int) int { return l.noiseStart() + l.noise + rate/4 }

func (l layout) stage(frame, rate int) (Stage, float64) {
	switch {
	case frame < l.leadIn:
		return StageLeadIn, float64(frame) / float64(l.leadIn)
	case frame < l.leadIn+l.sweep:
		return StageSweep, float64(frame-l.leadIn) / float64(l.sweep)
	case frame < l.noiseStart():
		return StageTail, float64(frame-l.leadIn-l.sweep) / float64(l.tail)
	default:
		return StageNoise, min(1, float64(frame-l.noiseStart())/float64(l.noise))
	}
}

// Run calibrates every mic in cfg.Stream.Inputs and returns the result. It
// does not save it; see Save.
//
// The loop is pull-based: each iteration asks cfg.Pump to advance the stream
// by 20 ms and then drains whatever the capture and reference rings hold. It
// ends when the rings hold the whole timeline, so it never depends on the
// wall clock except through the pump, and a test with a fake backend runs
// exactly the code a real device runs.
func Run(ctx context.Context, backend audioio.Backend, cfg Config) (Result, error) {
	cfg.defaults()
	devs, err := backend.Devices()
	if err != nil {
		return Result{}, fmt.Errorf("list audio devices: %w", err)
	}

	scfg := cfg.Stream
	amp := math.Pow(10, cfg.LevelDBFS/20)
	// The rate must be known to build the signal; the stream reports what was
	// granted, so a first open with a silent source would be needed for
	// SampleRate 0. The device list carries the current rate instead.
	rate := scfg.SampleRate
	if rate == 0 {
		rate = deviceRate(devs, scfg)
	}
	if rate == 0 {
		return Result{}, errors.New("cannot tell the device sample rate; set Stream.SampleRate")
	}
	lay := layout{
		leadIn: frames(cfg.LeadIn, rate), sweep: frames(cfg.SweepDuration, rate),
		tail: frames(cfg.TailSilence, rate), noise: frames(cfg.NoiseDuration, rate),
	}
	sweep, err := dsp.NewSweep(sweepLowHz, sweepHighHz, float64(lay.sweep)/float64(rate), float64(rate))
	if err != nil {
		return Result{}, fmt.Errorf("build calibration sweep: %w", err)
	}
	total := lay.total(rate)
	mono := make([]float32, total)
	for i, v := range sweep.Signal() {
		mono[lay.sweepStart()+i] = float32(amp) * v
	}
	copy(mono[lay.noiseStart():lay.noiseStart()+lay.noise], pinkNoise(lay.noise, rate, amp))
	stereo := make([]float32, 2*total)
	for i, v := range mono {
		stereo[2*i], stereo[2*i+1] = v, v
	}

	scfg.SampleRate = rate
	scfg.Source = audioio.NewBufferSource(stereo)
	stream, err := backend.Open(scfg)
	if err != nil {
		return Result{}, fmt.Errorf("open calibration stream: %w", err)
	}
	defer stream.Close()
	if got := stream.SampleRate(); got != rate {
		return Result{}, fmt.Errorf("device runs at %d Hz, not the %d Hz the calibration was built for", got, rate)
	}
	if err := stream.Start(); err != nil {
		return Result{}, fmt.Errorf("start calibration stream: %w", err)
	}
	defer stream.Stop()

	ref, caps, err := record(ctx, stream, len(scfg.Inputs), total, lay, cfg)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		Device: deviceName(devs, scfg), SampleRate: rate, Outputs: scfg.Outputs, Time: cfg.Now(),
	}
	for i, ch := range scfg.Inputs {
		cfg.Progress(StageAnalysis, float64(i)/float64(len(scfg.Inputs)))
		in, err := analyse(ctx, sweep, lay, rate, amp, ref, caps[i])
		if err != nil {
			return Result{}, fmt.Errorf("input %d: %w", ch, err)
		}
		in.Channel = ch
		res.Inputs = append(res.Inputs, in)
	}
	cfg.Progress(StageAnalysis, 1)
	return res, nil
}

// record pumps the stream until the reference and every capture ring have
// delivered total frames, and returns them.
func record(ctx context.Context, s audioio.Stream, inputs, total int, lay layout, cfg Config) (ref []float32, caps [][]float32, err error) {
	rate := s.SampleRate()
	step := rate * stepMillis / 1000
	chunk := make([]float32, 2*rate)
	caps = make([][]float32, inputs)
	drain := func(dst []float32, r *audioio.Ring) []float32 {
		for {
			n := r.Read(chunk)
			if n == 0 {
				return dst
			}
			dst = append(dst, chunk[:n]...)
		}
	}
	ref = make([]float32, 0, total)
	for i := range caps {
		caps[i] = make([]float32, 0, total)
	}
	idle := 0
	for {
		got := len(ref)
		for _, c := range caps {
			got = min(got, len(c))
		}
		if got >= total {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := cfg.Pump(ctx, s, min(step, total-got)); err != nil {
			return nil, nil, err
		}
		before := len(ref)
		ref = drain(ref, s.Reference())
		for i := range caps {
			caps[i] = drain(caps[i], s.Capture(i))
		}
		if len(ref) == before {
			idle++
			if time.Duration(idle*step)*time.Second/time.Duration(rate) > stallLimit {
				return nil, nil, errors.New("the audio stream stopped delivering samples")
			}
		} else {
			idle = 0
		}
		stage, frac := lay.stage(min(len(ref), total-1), rate)
		cfg.Progress(stage, frac)
	}
	st := s.Stats()
	if n := st.CaptureOverflows.Load() + st.ReferenceOverflows.Load(); n > 0 {
		return nil, nil, fmt.Errorf("audio was dropped (%d samples): the machine was too busy, close other programs and try again", n)
	}
	if n := st.SourceUnderruns.Load(); n > 0 {
		return nil, nil, fmt.Errorf("playback underran (%d frames): try again", n)
	}
	ref = ref[:total]
	for i := range caps {
		caps[i] = caps[i][:total]
	}
	return ref, caps, nil
}

// analyse turns one mic's recording into an Input.
func analyse(ctx context.Context, sweep *dsp.Sweep, lay layout, rate int, amp float64, ref, mic []float32) (Input, error) {
	var in Input
	// Noise floor from the lead-in, skipping the first 100 ms in which the
	// stream may still be settling.
	quiet := mic[min(rate/10, lay.leadIn/2):lay.leadIn]
	in.NoiseFloorDBFS = dbfs(meanSquare(quiet))

	sweepEnd := lay.sweepStart() + lay.sweep
	peakIn := 0.0
	for _, v := range mic[lay.sweepStart():sweepEnd] {
		peakIn = math.Max(peakIn, math.Abs(float64(v)))
	}
	if peakIn >= clipLevel {
		return in, fmt.Errorf("%w (peak %.2f of full scale): lower the input gain on the interface or the speaker volume and calibrate again", ErrClipping, peakIn)
	}
	if err := ctx.Err(); err != nil {
		return in, err
	}

	// Impulse response at the device rate, indexed from the moment the sweep
	// began playing, then brought to 16 kHz.
	ir48, err := sweep.ImpulseResponse(mic[lay.sweepStart():sweepEnd+lay.tail], lay.tail)
	if err != nil {
		return in, fmt.Errorf("deconvolve the sweep: %w", err)
	}
	for i := range ir48 {
		ir48[i] /= float32(amp)
	}
	ir16, err := toPipelineRate(ir48, rate)
	if err != nil {
		return in, err
	}

	peak, floor := 0.0, robustSigma(ir16)
	peakIdx := 0
	for i, v := range ir16 {
		if a := math.Abs(float64(v)); a > peak {
			peak, peakIdx = a, i
		}
	}
	if floor == 0 || peak/floor < minPeakToNoise {
		in.NoEchoPath = true
		return in, nil
	}

	// Delay by GCC-PHAT on the noise, which is broadband and stationary, with
	// the peak of the impulse response as a cross-check.
	ref16, mic16, err := bothToPipelineRate(ref, mic, rate)
	if err != nil {
		return in, err
	}
	n0 := lay.noiseStart() * dsp.PipelineRate / rate
	n1 := min(len(ref16), len(mic16), (lay.noiseStart()+lay.noise)*dsp.PipelineRate/rate)
	est, err := dsp.NewDelayEstimator(dsp.PipelineRate/2, n1-n0)
	if err != nil {
		return in, fmt.Errorf("delay estimator: %w", err)
	}
	gcc, err := est.Estimate(ref16[n0:n1], mic16[n0:n1])
	if err != nil {
		return in, fmt.Errorf("estimate the delay: %w", err)
	}
	irPeak := refinePeak(ir16, peakIdx)
	in.DelaySamples = gcc.Delay
	if gcc.Confidence < 2 {
		in.DelaySamples = irPeak
		in.Warnings = append(in.Warnings, "delay taken from the impulse response: the noise correlation was weak")
	} else if math.Abs(gcc.Delay-irPeak) > delayDisagreeSamples {
		in.Warnings = append(in.Warnings, fmt.Sprintf("delay estimates disagree: %.1f ms by correlation, %.1f ms by impulse response peak",
			1000*gcc.Delay/dsp.PipelineRate, 1000*irPeak/dsp.PipelineRate))
	}
	in.DelayMs = 1000 * in.DelaySamples / dsp.PipelineRate
	in.BulkDelay = max(0, int(math.Floor(in.DelaySamples))-preRoll)

	onset, tailSec := dsp.EchoTail(ir16, dsp.PipelineRate)
	tailSec = math.Min(math.Max(tailSec, minTail.Seconds()), maxTail.Seconds())
	in.TailMs = 1000 * tailSec
	end := min(len(ir16), max(onset, int(in.DelaySamples))+int(math.Round(tailSec*dsp.PipelineRate)))
	if end <= in.BulkDelay {
		return in, fmt.Errorf("the echo starts at %d samples but the impulse response ends at %d", in.BulkDelay, end)
	}
	in.IR = slices.Clone(ir16[in.BulkDelay:end])

	// Cancel the noise with a canceller seeded from that response.
	c, err := in.NewCanceller()
	if err != nil {
		return in, err
	}
	from := max(n0, n0+int(settle.Seconds()*dsp.PipelineRate))
	var micPow, outPow float64
	var count int
	out := make([]float32, dsp.CancellerBlock)
	for p := n0; p+dsp.CancellerBlock <= n1; p += dsp.CancellerBlock {
		c.Process(ref16[p:p+dsp.CancellerBlock], mic16[p:p+dsp.CancellerBlock], out, nil)
		if p < from {
			continue
		}
		for i, v := range out {
			m := float64(mic16[p+i])
			micPow += m * m
			outPow += float64(v) * float64(v)
		}
		count += len(out)
	}
	if count == 0 {
		return in, errors.New("the noise segment is too short to measure the residual")
	}
	in.ResidualFloorDBFS = dbfs(outPow / float64(count))
	in.ERLEdB = dbfs(micPow/float64(count)) - in.ResidualFloorDBFS
	return in, nil
}

// SeedIR is the impulse response indexed from the moment the reference was
// played, as Canceller.SeedImpulseResponse expects: the first BulkDelay
// samples are zeros. They are not stored, since a delay of tens of
// milliseconds would otherwise dominate the file.
func (in Input) SeedIR() []float32 {
	return append(make([]float32, in.BulkDelay), in.IR...)
}

// NewCanceller builds a canceller for this mic, already seeded from the
// calibration. Production always seeds: an unseeded filter needs seconds to
// converge on music. It fails for a mic with no echo path.
func (in Input) NewCanceller() (*dsp.Canceller, error) {
	if in.NoEchoPath || len(in.IR) == 0 {
		return nil, fmt.Errorf("input %d has no calibrated echo path", in.Channel)
	}
	c, err := dsp.NewCanceller(dsp.CancellerConfig{Tail: len(in.IR), BulkDelay: in.BulkDelay})
	if err != nil {
		return nil, fmt.Errorf("build canceller: %w", err)
	}
	c.SeedImpulseResponse(in.SeedIR())
	return c, nil
}

// Usable lists the inputs that have an echo path and can be seeded.
func (r Result) Usable() []Input {
	var out []Input
	for _, in := range r.Inputs {
		if !in.NoEchoPath {
			out = append(out, in)
		}
	}
	return out
}

func frames(d time.Duration, rate int) int { return int(d.Seconds() * float64(rate)) }

func meanSquare(x []float32) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return s / float64(len(x))
}

// dbfs is the level of a mean square in dB relative to full scale RMS.
func dbfs(meanSq float64) float64 { return 10 * math.Log10(meanSq+1e-20) }

// robustSigma estimates the noise level of x from its median absolute value,
// which an echo's few large samples cannot pull up the way an RMS would.
func robustSigma(x []float32) float64 {
	abs := make([]float64, len(x))
	for i, v := range x {
		abs[i] = math.Abs(float64(v))
	}
	slices.Sort(abs)
	return abs[len(abs)/2] / 0.6745
}

// refinePeak places the peak at a fraction of a sample by a parabola through
// its neighbours.
func refinePeak(x []float32, i int) float64 {
	if i < 1 || i >= len(x)-1 {
		return float64(i)
	}
	l, m, r := math.Abs(float64(x[i-1])), math.Abs(float64(x[i])), math.Abs(float64(x[i+1]))
	if den := l - 2*m + r; den < 0 {
		return float64(i) + 0.5*(l-r)/den
	}
	return float64(i)
}

// pinkNoise is deterministic pink noise (Paul Kellet's filter) scaled to the
// given RMS. Pink noise has the long-term spectrum of music, so the canceller
// is tested on something as demanding as the songs it will meet. The seed is
// fixed so two calibrations play the same signal.
//
// The noise is high-passed at the bottom of the sweep band: the impulse
// response is only measured down to there, and small speakers reproduce next
// to nothing below it, so energy under 50 Hz would only measure how much the
// canceller lacks a response that the real echo does not have.
func pinkNoise(n, rate int, rms float64) []float32 {
	rng := rand.New(rand.NewSource(1))
	var b0, b1, b2, b3, b4, b5, b6 float64
	out := make([]float64, n)
	var sum float64
	for i := range out {
		w := rng.NormFloat64()
		b0 = 0.99886*b0 + w*0.0555179
		b1 = 0.99332*b1 + w*0.0750759
		b2 = 0.96900*b2 + w*0.1538520
		b3 = 0.86650*b3 + w*0.3104856
		b4 = 0.55000*b4 + w*0.5329522
		b5 = -0.7616*b5 - w*0.0168980
		out[i] = b0 + b1 + b2 + b3 + b4 + b5 + b6 + w*0.5362
		b6 = w * 0.115926
	}
	highPass(out, sweepLowHz, float64(rate))
	for _, v := range out {
		sum += v * v
	}
	g := rms / math.Sqrt(sum/float64(n))
	res := make([]float32, n)
	for i, v := range out {
		res[i] = float32(math.Max(-0.95, math.Min(0.95, v*g)))
	}
	return res
}

func deviceRate(devs []audioio.Device, cfg audioio.StreamConfig) int {
	if d, ok := pickDevice(devs, cfg); ok {
		return d.DefaultRate
	}
	return 0
}

func deviceName(devs []audioio.Device, cfg audioio.StreamConfig) string {
	if d, ok := pickDevice(devs, cfg); ok {
		return d.Name
	}
	return cmpOr(cfg.DeviceName, "default")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// pickDevice mirrors how a backend resolves a config: ID, then name, then the
// default device.
func pickDevice(devs []audioio.Device, cfg audioio.StreamConfig) (audioio.Device, bool) {
	for _, d := range devs {
		if cfg.DeviceID != "" && d.ID == cfg.DeviceID {
			return d, true
		}
	}
	if cfg.DeviceID == "" {
		for _, d := range devs {
			if cfg.DeviceName != "" && strings.EqualFold(d.Name, cfg.DeviceName) {
				return d, true
			}
		}
	}
	if cfg.DeviceID == "" && cfg.DeviceName == "" {
		for _, d := range devs {
			if d.IsDefault {
				return d, true
			}
		}
		if len(devs) > 0 {
			return devs[0], true
		}
	}
	return audioio.Device{}, false
}
