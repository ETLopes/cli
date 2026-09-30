package live

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/dsp"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/score"
)

const (
	// chunkFrames bounds how many device frames are fed to the pipeline at
	// once, and so the size of every scratch buffer.
	chunkFrames = 4096
	// histSize is the 16 kHz history kept for level measurements. A frame
	// window is dsp.GateWindow samples and at most the chain's block latency
	// old, far inside it. A power of two.
	histSize = 4096
	// refActivePower is the mean square (-50 dBFS) above which the backing
	// counts as playing, for the echo-reduction metric.
	refActivePower = 1e-5
	// noEchoTail is the canceller tail used for a mic with no calibrated
	// echo path (200 ms at 16 kHz): it is unseeded but still there.
	noEchoTail = 3200
)

// lane is the processing path of one microphone.
type lane struct {
	idx     int
	name    string
	channel int
	noEcho  bool
	// delayDev is the calibrated round trip in device frames.
	delayDev float64

	res    *dsp.Resampler
	chain  *dsp.Chain
	pend   []float32
	hist   [histSize]float32
	scorer *score.Scorer

	// Metrics, touched by the processing goroutine only and read after it ended.
	frames, voiced, echoFrames int
	micPower                   float64 // sum over all frames of the mic window mean square
	echoMic, echoClean         float64 // sums over the echo frames

	// Live view for Snapshot.
	sungMIDI atomic.Uint64
	level    atomic.Uint64
	isVoiced atomic.Bool
}

type skip struct {
	at  int64   // 16 kHz sample index from which the gap applies
	cum float64 // device frames lost so far
}

// pipeline is the per-sample processing shared by a live session and a
// replay: resample the reference and every mic to 16 kHz, run each chain,
// convert each pitch frame to song time and score it. Live and replay both
// call feed with the same kind of slices, so they run identical code.
type pipeline struct {
	rate   int
	gd     float64 // resampler group delay, 16 kHz samples
	refRes *dsp.Resampler
	// refPend and refHist are the reference counterparts of lane.pend and hist.
	refPend []float32
	refHist [histSize]float32
	lanes   []*lane
	tl      *timeline

	produced int64 // 16 kHz samples produced by the resamplers
	fed      int64 // 16 kHz samples given to the chains
	skips    []skip

	// tap, if set, sees every frame pushed to a scorer. Tests use it to
	// compare a live run with a batch score of the same frames.
	tap func(lane int, f score.Frame)
}

// songData is what a session and a replay load from a song directory.
type songData struct {
	grid  reference.Grid
	lines []lyrics.Line
}

type laneSpec struct {
	name    string
	channel int
}

// newPipeline builds the per-mic paths. Every mic is seeded from its
// calibration; a mic flagged NoEchoPath gets an unseeded chain that still
// sings.
func newPipeline(rate int, cal calibrate.Result, specs []laneSpec, data songData, cfg score.Config, tl *timeline) (*pipeline, error) {
	refRes, err := dsp.NewResampler(rate)
	if err != nil {
		return nil, fmt.Errorf("reference resampler: %w", err)
	}
	pendCap := chunkFrames*dsp.PipelineRate/rate + 2*dsp.CancellerBlock
	p := &pipeline{rate: rate, gd: refRes.GroupDelay(), refRes: refRes, refPend: make([]float32, 0, pendCap), tl: tl}
	for _, sp := range specs {
		in, ok := inputFor(cal, sp.channel)
		if !ok {
			return nil, fmt.Errorf("the calibration has no input %d (it has %s)", sp.channel, channelList(cal))
		}
		cc := dsp.ChainConfig{Tail: noEchoTail}
		l := &lane{idx: len(p.lanes), name: sp.name, channel: sp.channel, noEcho: in.NoEchoPath}
		if in.NoEchoPath {
			// The floor of the mic itself: nothing was heard from the speakers.
			cc.ResidualFloor = math.Pow(10, in.NoiseFloorDBFS/20)
		} else {
			cc.Tail, cc.BulkDelay, cc.ImpulseResponse = len(in.IR), in.BulkDelay, in.SeedIR()
			cc.ResidualFloor = math.Pow(10, in.ResidualFloorDBFS/20)
			l.delayDev = in.DelaySamples * float64(rate) / dsp.PipelineRate
		}
		if l.chain, err = dsp.NewChain(cc); err != nil {
			return nil, fmt.Errorf("%s: %w", sp.name, err)
		}
		if l.res, err = dsp.NewResampler(rate); err != nil {
			return nil, fmt.Errorf("%s: %w", sp.name, err)
		}
		l.pend = make([]float32, 0, pendCap)
		l.scorer = score.New(data.grid, data.lines, cfg)
		p.lanes = append(p.lanes, l)
	}
	return p, nil
}

func inputFor(cal calibrate.Result, channel int) (calibrate.Input, bool) {
	for _, in := range cal.Inputs {
		if in.Channel == channel {
			return in, true
		}
	}
	return calibrate.Input{}, false
}

func channelList(cal calibrate.Result) string {
	s := ""
	for i, in := range cal.Inputs {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprint(in.Channel)
	}
	if s == "" {
		return "none"
	}
	return s
}

// skip records that n device frames were lost between the data fed so far and
// the data fed next (a ring overflowed), so later frames keep their true
// stream time.
func (p *pipeline) skip(n int64) {
	if n <= 0 {
		return
	}
	cum := float64(n)
	if len(p.skips) > 0 {
		cum += p.skips[len(p.skips)-1].cum
	}
	p.skips = append(p.skips, skip{at: p.produced, cum: cum})
}

func (p *pipeline) skipped(center int) float64 {
	for i := len(p.skips) - 1; i >= 0; i-- {
		if p.skips[i].at <= int64(center) {
			return p.skips[i].cum
		}
	}
	return 0
}

// feed processes n = len(ref) device frames of the reference and of every mic
// (mics[i] belongs to lane i). n must not exceed chunkFrames. It allocates
// nothing once the scratch buffers have their final size, apart from the
// scorer (see noScore).
func (p *pipeline) feed(ref []float32, mics [][]float32) {
	before := len(p.refPend)
	p.refPend = p.refRes.Process(p.refPend, ref)
	p.produced += int64(len(p.refPend) - before)
	for i, l := range p.lanes {
		l.pend = l.res.Process(l.pend, mics[i])
	}

	const b = dsp.CancellerBlock
	used := 0
	for len(p.refPend)-used >= b {
		refBlk := p.refPend[used : used+b]
		for j := 0; j < b; j++ {
			p.refHist[(int(p.fed)+j)&(histSize-1)] = refBlk[j]
		}
		for _, l := range p.lanes {
			micBlk := l.pend[used : used+b]
			for j := 0; j < b; j++ {
				l.hist[(int(p.fed)+j)&(histSize-1)] = micBlk[j]
			}
			if f, ok := l.chain.Process(refBlk, micBlk); ok {
				p.emit(l, f)
			}
		}
		p.fed += b
		used += b
	}
	p.refPend = p.refPend[:copy(p.refPend, p.refPend[used:])]
	for _, l := range p.lanes {
		l.pend = l.pend[:copy(l.pend, l.pend[used:])]
	}
}

// meanSquare is the mean square of the n samples of h starting at index start.
func meanSquare(h *[histSize]float32, start, n int) float64 {
	var s float64
	for i := 0; i < n; i++ {
		v := float64(h[(start+i)&(histSize-1)])
		s += v * v
	}
	return s / float64(n)
}

// emit turns one pitch frame of a mic into a scored frame and metrics.
//
// Song time of a frame, in device frames of the stream clock:
//
//	stream = (Center - groupDelay) * rate/16000 + skipped - roundTrip
//	song   = timeline(stream)
//
// Center is the frame's middle in 16 kHz mic samples from the first sample;
// dsp.Chain already reports it on the microphone's own timeline, i.e. with
// the residual suppressor's one block of latency compensated, so nothing is
// subtracted for that here. groupDelay is the resampler's delay (identical
// for the reference and the mic, so it shifts both equally, but the mic
// sample k holds what the device captured at k minus this delay). roundTrip
// is the calibrated playback-to-mic delay: what the singer sings at song
// position s is captured roundTrip frames after the source played s, because
// the singer hears the song through the same speakers and interface.
// skipped adds the device frames a ring overflow lost before this frame.
func (p *pipeline) emit(l *lane, f dsp.Frame) {
	start := f.Index * dsp.GateHop
	micPow := meanSquare(&l.hist, start, dsp.GateWindow)
	refPow := meanSquare(&p.refHist, start, dsp.GateWindow)

	l.frames++
	l.micPower += micPow
	if f.Voiced {
		l.voiced++
	} else if refPow > refActivePower {
		l.echoFrames++
		l.echoMic += micPow
		l.echoClean += f.Level * f.Level
	}

	center := f.Center()
	scale := float64(p.rate) / dsp.PipelineRate
	stream := (float64(center)-p.gd)*scale + p.skipped(center) - l.delayDev
	sf := score.Frame{Voiced: f.Voiced}
	if f.Voiced && f.F0 > 0 {
		sf.MIDI = reference.HzToMIDI(f.F0)
	} else {
		sf.Voiced = false
	}
	song, ok := p.tl.songFrame(stream)

	l.level.Store(math.Float64bits(dbfs(micPow)))
	l.sungMIDI.Store(math.Float64bits(sf.MIDI))
	l.isVoiced.Store(sf.Voiced)
	if !ok {
		return
	}
	sf.T = time.Duration(song / float64(p.rate) * float64(time.Second))
	if p.tap != nil {
		p.tap(l.idx, sf)
	}
	l.scorer.Push(sf)
}

func dbfs(meanSq float64) float64 { return 10 * math.Log10(meanSq+1e-12) }

// finish finalizes every scorer. incomplete selects FinishEarly.
func (p *pipeline) finish(incomplete bool) []score.Result {
	out := make([]score.Result, len(p.lanes))
	for i, l := range p.lanes {
		if incomplete {
			out[i] = l.scorer.FinishEarly()
		} else {
			out[i] = l.scorer.Finish()
		}
	}
	return out
}

// Metrics summarizes what the chains made of a session, per mic, for tuning
// the canceller on real recordings.
type Metrics struct {
	Mics []MicMetrics
}

// MicMetrics is the outcome of one mic's chain.
type MicMetrics struct {
	Name    string
	Channel int
	// Frames is the number of 10 ms pitch frames the chain emitted.
	Frames int
	// VoicedFraction is the share of frames the gate accepted as singing.
	VoicedFraction float64
	// EchoFrames counts the frames the gate rejected while the backing was
	// playing, and EchoReductionDB is the mic power over the cleaned power
	// on those frames, in dB (the ERLE the whole chain achieved). It is 0
	// when there were none.
	EchoFrames      int
	EchoReductionDB float64
	// MeanInputDBFS is the mean-square level of the raw mic over all frames.
	MeanInputDBFS float64
}

func (p *pipeline) metrics() Metrics {
	var m Metrics
	for _, l := range p.lanes {
		mm := MicMetrics{Name: l.name, Channel: l.channel, Frames: l.frames, EchoFrames: l.echoFrames}
		if l.frames > 0 {
			mm.VoicedFraction = float64(l.voiced) / float64(l.frames)
			mm.MeanInputDBFS = dbfs(l.micPower / float64(l.frames))
		}
		if l.echoFrames > 0 {
			mm.EchoReductionDB = 10 * math.Log10((l.echoMic+1e-20)/(l.echoClean+1e-20))
		}
		m.Mics = append(m.Mics, mm)
	}
	return m
}
