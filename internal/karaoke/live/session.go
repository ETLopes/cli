// Package live runs a karaoke song in real time: it plays the instrumental,
// captures every selected mic on the same duplex stream, cleans the backing
// track's echo out of each mic, tracks the singer's pitch and scores it.
//
// One goroutine does all the processing. It pulls the stream forward (a
// sleep on a real device, a Step on the fake one, both behind Pump), drains
// the reference and capture rings, and feeds the same per-block pipeline a
// replay of a recorded session runs, so a recording reproduces a live run.
package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/dsp"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

// instrumentalRate is the rate of song's instrumental.wav.
const instrumentalRate = 48000

// gridStep is the scoring grid: the scorer compares the singer every 10 ms.
const gridStep = 10 * time.Millisecond

// stepMillis is how much device time the loop advances per iteration.
const stepMillis = 20

// Pump advances the stream by frames frames of device time and returns when
// they have elapsed. It is the pattern of calibrate.Config.Pump: the default
// sleeps, because a real device advances itself, and a test calls
// audioio.FakeStream.Step so the same loop runs in both worlds.
type Pump func(ctx context.Context, s audioio.Stream, frames int) error

// Renderer converts audio files. *audio.Converter is the production one; it
// is an interface so a test can substitute ffmpeg.
type Renderer interface {
	RenderWAV(ctx context.Context, src, dst string, spec audio.WAVSpec, total time.Duration, onProgress audio.ProgressFunc) error
}

// SessionConfig configures Open.
type SessionConfig struct {
	Song        song.Song
	Calibration calibrate.Result
	// CalibrationStale, if not empty, is shown as a warning: the caller knows
	// the calibration is old or the setup has changed.
	CalibrationStale string
	Backend          audioio.Backend
	// Stream selects the device and, in Inputs, the mics. Outputs and
	// SampleRate default to the calibration's; if set they must agree with it.
	// Source is ignored: the session plays the song.
	Stream audioio.StreamConfig
	// Players names the mics, in Stream.Inputs order.
	Players    []string
	Difficulty score.Difficulty
	Slack      time.Duration
	// RecordDir, if not empty, receives a replayable bundle (see Recorder).
	RecordDir string
	// Pump nil sleeps.
	Pump Pump
	// Renderer is only needed when the device does not run at 48 kHz.
	Renderer Renderer
}

// PlayerResult is one player's outcome.
type PlayerResult struct {
	Name    string
	Channel int
	score.Result
}

// Results are the final scores of a session.
type Results struct {
	Players []PlayerResult
	// Incomplete is set when the session was stopped before the song ended.
	Incomplete bool
}

// StreamStats are the stream counters, in frames.
type StreamStats struct {
	Frames, CaptureOverflows, ReferenceOverflows, SourceUnderruns uint64
}

// PlayerSnapshot is the live view of one player.
type PlayerSnapshot struct {
	Name    string
	Channel int
	Score   int
	Streak  time.Duration
	// Last is whether the latest judged frame was a hit or a miss.
	Last score.Event
	// SungMIDI is the raw detected pitch (not octave folded) and Voiced says
	// whether the gate accepted it as singing.
	SungMIDI float64
	Voiced   bool
	// TargetMIDI is the reference pitch at the player's latest time.
	TargetMIDI   float64
	TargetVoiced bool
	// LevelDBFS is the raw mic level over the latest frame window.
	LevelDBFS float64
}

// Snapshot is the cheap view a UI polls.
type Snapshot struct {
	Position, Duration time.Duration
	// LineIndex is the lyric line being sung, -1 if none, and LineProgress how
	// far through it the song is, 0..1.
	LineIndex    int
	LineProgress float64
	Players      []PlayerSnapshot
	Stream       StreamStats
	Paused, Done bool
	Warnings     []string
}

// Session is a running song.
type Session struct {
	cfg    SessionConfig
	rate   int
	stream audioio.Stream
	src    *audioio.BufferSource
	data   songData
	pump   Pump
	step   int

	// mu guards the timeline and the processing state below, so a Pause takes
	// effect between two iterations and never inside one.
	mu           sync.Mutex
	tl           *timeline
	pipe         *pipeline
	rec          *recorder
	consumed     int64
	lastOverflow uint64
	endAt        int64
	refBuf       []float32
	micBufs      [][]float32
	views        [][]float32

	srcFrames  int64
	tailFrames int64
	warnings   []string

	cancel  context.CancelFunc
	done    chan struct{}
	stopped atomic.Bool
	paused  atomic.Bool
	fin     atomic.Bool
	results Results
	err     error
	closed  sync.Once
}

// Open loads the song, opens the stream and starts playing and scoring.
func Open(ctx context.Context, cfg SessionConfig) (*Session, error) {
	s, err := newSession(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := s.stream.Start(); err != nil {
		s.stream.Close()
		return nil, fmt.Errorf("start audio stream: %w", err)
	}
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
	return s, nil
}

// newSession does everything but start the stream and the loop.
func newSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if len(cfg.Stream.Inputs) == 0 {
		return nil, errors.New("no input channels selected")
	}
	if len(cfg.Players) != len(cfg.Stream.Inputs) {
		return nil, fmt.Errorf("%d player names for %d inputs", len(cfg.Players), len(cfg.Stream.Inputs))
	}
	cal := cfg.Calibration
	rate := cal.SampleRate
	if cfg.Stream.SampleRate != 0 && cfg.Stream.SampleRate != rate {
		return nil, fmt.Errorf("the calibration is for %d Hz but the stream asks for %d Hz", rate, cfg.Stream.SampleRate)
	}
	if cfg.Stream.Outputs != ([2]int{}) && cfg.Stream.Outputs != cal.Outputs {
		return nil, fmt.Errorf("the calibration is for outputs %v but the stream uses %v", cal.Outputs, cfg.Stream.Outputs)
	}
	if rate < dsp.PipelineRate {
		return nil, fmt.Errorf("calibration sample rate %d Hz is not usable", rate)
	}

	grid, err := loadGrid(cfg.Song)
	if err != nil {
		return nil, err
	}
	var lines []lyrics.Line
	if l, ok, err := cfg.Song.LoadLyrics(); err != nil {
		return nil, err
	} else if ok {
		lines = l.Lines
	}
	mono, err := loadInstrumental(ctx, cfg.Song, rate, cfg.Renderer)
	if err != nil {
		return nil, err
	}
	stereo := make([]float32, 2*len(mono))
	for i, v := range mono {
		stereo[2*i], stereo[2*i+1] = v, v
	}

	s := &Session{cfg: cfg, rate: rate, data: songData{grid: grid, lines: lines}, tl: newTimeline(),
		src: audioio.NewBufferSource(stereo), pump: cfg.Pump, done: make(chan struct{}), cancel: func() {}}
	if s.pump == nil {
		s.pump = sleepPump
	}
	s.step = rate * stepMillis / 1000
	s.srcFrames = int64(len(mono))

	specs := make([]laneSpec, len(cfg.Players))
	for i, name := range cfg.Players {
		specs[i] = laneSpec{name: name, channel: cfg.Stream.Inputs[i]}
	}
	s.pipe, err = newPipeline(rate, cal, specs, s.data, score.Config{Difficulty: cfg.Difficulty, Slack: cfg.Slack}, s.tl)
	if err != nil {
		return nil, err
	}
	var maxDelay float64
	for _, l := range s.pipe.lanes {
		maxDelay = max(maxDelay, l.delayDev)
		if l.noEcho {
			s.warnings = append(s.warnings, fmt.Sprintf("%s: no echo path detected on input %d; the backing track is not cancelled on that mic", l.name, l.channel))
		}
	}
	// After the song ends the chains still owe frames: the analysis window,
	// the suppressor block and the echo's round trip, plus the scorer slack.
	slack := cfg.Slack
	if slack <= 0 {
		slack = score.DefaultSlack
	}
	latency := float64(dsp.GateWindow+3*dsp.CancellerBlock) / dsp.PipelineRate
	s.tailFrames = int64(maxDelay) + int64((latency+slack.Seconds()+0.05)*float64(rate))
	for _, in := range cal.Inputs {
		for _, w := range in.Warnings {
			s.warnings = append(s.warnings, fmt.Sprintf("calibration input %d: %s", in.Channel, w))
		}
	}
	if cfg.CalibrationStale != "" {
		s.warnings = append(s.warnings, "calibration: "+cfg.CalibrationStale)
	}

	scfg := cfg.Stream
	scfg.SampleRate, scfg.Outputs, scfg.Source = rate, cal.Outputs, s.src
	stream, err := cfg.Backend.Open(scfg)
	if err != nil {
		return nil, fmt.Errorf("open audio stream: %w", err)
	}
	if got := stream.SampleRate(); got != rate {
		stream.Close()
		return nil, fmt.Errorf("the device runs at %d Hz, not the %d Hz it was calibrated at", got, rate)
	}
	s.stream = stream

	s.refBuf = make([]float32, chunkFrames)
	s.micBufs = make([][]float32, len(specs))
	for i := range s.micBufs {
		s.micBufs[i] = make([]float32, chunkFrames)
	}
	if cfg.RecordDir != "" {
		var werr error
		if s.rec, werr = newRecorder(cfg.RecordDir, rate, cfg.Stream.Inputs); werr != nil {
			s.warnings = append(s.warnings, "recording disabled: "+werr.Error())
		}
	}
	return s, nil
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

func loadGrid(sg song.Song) (reference.Grid, error) {
	c, err := reference.Load(sg.ReferencePath())
	if err != nil {
		return reference.Grid{}, fmt.Errorf("load the reference pitch: %w", err)
	}
	return c.Grid(gridStep), nil
}

// loadInstrumental returns the mono downmix of the backing track at the device
// rate. At 48 kHz that is a Go downmix of instrumental.wav; at any other rate
// ffmpeg renders (once) instrumental-mono-<rate>.wav beside it.
func loadInstrumental(ctx context.Context, sg song.Song, rate int, r Renderer) ([]float32, error) {
	path := sg.InstrumentalPath()
	if rate != instrumentalRate {
		path = filepath.Join(sg.Dir, fmt.Sprintf("instrumental-mono-%d.wav", rate))
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			if r == nil {
				return nil, fmt.Errorf("the device runs at %d Hz and no renderer is available to convert the instrumental", rate)
			}
			spec := audio.WAVSpec{SampleRate: rate, Channels: 1, Format: audio.SampleFloat32}
			if err := r.RenderWAV(ctx, sg.InstrumentalPath(), path, spec, sg.Duration(), nil); err != nil {
				return nil, fmt.Errorf("render the instrumental at %d Hz: %w", rate, err)
			}
		}
	}
	a, err := audioio.ReadWAVFile(path)
	if err != nil {
		return nil, fmt.Errorf("load the instrumental: %w", err)
	}
	if a.SampleRate != rate {
		return nil, fmt.Errorf("%s is %d Hz, want %d Hz", filepath.Base(path), a.SampleRate, rate)
	}
	n := a.Frames()
	mono := make([]float32, n)
	for i := range mono {
		var sum float32
		for c := 0; c < a.Channels; c++ {
			sum += a.Samples[i*a.Channels+c]
		}
		mono[i] = sum / float32(a.Channels)
	}
	return mono, nil
}

// run is the processing goroutine.
func (s *Session) run(ctx context.Context) {
	defer close(s.done)
	var runErr error
	natural := false
	for {
		fin, err := s.iterate(ctx)
		if err != nil {
			runErr = err
			break
		}
		if fin {
			natural = true
			break
		}
	}
	s.stream.Stop()

	s.mu.Lock()
	res := s.pipe.finish(!natural)
	s.results = Results{Incomplete: !natural}
	for i, l := range s.pipe.lanes {
		s.results.Players = append(s.results.Players, PlayerResult{Name: l.name, Channel: l.channel, Result: res[i]})
	}
	if s.rec != nil {
		s.rec.finish(s.bundle(!natural))
	}
	s.mu.Unlock()
	if errors.Is(runErr, context.Canceled) && s.stopped.Load() {
		runErr = nil
	}
	s.err = runErr
	s.fin.Store(true)
}

// iterate advances the stream one step and processes what arrived. It reports
// whether the song and the chains' tail are complete.
func (s *Session) iterate(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := s.pump(ctx, s.stream, s.step); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stream.Stats()
	overflow := st.CaptureOverflows.Load() + st.ReferenceOverflows.Load()

	avail := s.stream.Reference().Len()
	for i := range s.micBufs {
		avail = min(avail, s.stream.Capture(i).Len())
	}
	for avail > 0 {
		n := min(avail, chunkFrames)
		got := s.stream.Reference().Read(s.refBuf[:n])
		for i := range s.micBufs {
			got = min(got, s.stream.Capture(i).Read(s.micBufs[i][:n]))
		}
		ref := s.refBuf[:got]
		mics := s.micViews(got)
		s.pipe.feed(ref, mics)
		s.rec.write(ref, mics)
		s.consumed += int64(got)
		avail -= n
	}
	if overflow != s.lastOverflow {
		s.lastOverflow = overflow
		// The rings drop the newest samples, so what was read is contiguous
		// and everything until the ring's next sample is lost.
		if gap := int64(st.Frames.Load()) - int64(s.stream.Reference().Len()) - s.consumed; gap > 0 {
			s.pipe.skip(gap)
			s.rec.gap(s.consumed, gap)
			s.consumed += gap
		}
	}

	frames := int64(st.Frames.Load())
	if s.endAt == 0 && s.src.Done() {
		s.endAt = frames + s.tailFrames
	}
	return s.endAt != 0 && s.consumed >= s.endAt, nil
}

// micViews returns the first n frames of each mic buffer. The slice of slices
// is reused, so it does not allocate after the first call.
func (s *Session) micViews(n int) [][]float32 {
	if s.views == nil {
		s.views = make([][]float32, len(s.micBufs))
	}
	for i := range s.views {
		s.views[i] = s.micBufs[i][:n]
	}
	return s.views
}

// Pause freezes the song. The stream keeps running (the source plays silence
// and its position stays put), so capture and reference stay sample-aligned;
// frames captured while paused are not scored.
func (s *Session) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tl.paused() || s.fin.Load() {
		return
	}
	s.src.Pause()
	s.tl.pause(int64(s.stream.Stats().Frames.Load()), s.src.Position())
	s.paused.Store(true)
}

// Resume continues the song from where Pause froze it.
func (s *Session) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tl.paused() || s.fin.Load() {
		return
	}
	s.tl.resume(int64(s.stream.Stats().Frames.Load()))
	s.src.Resume()
	s.paused.Store(false)
}

// Stop ends the session early. The results are flagged incomplete unless the
// song had already ended.
func (s *Session) Stop() (Results, error) {
	s.stopped.Store(true)
	s.cancel()
	return s.Wait()
}

// Wait blocks until the song has ended (or Stop, or a failure) and returns the
// final results.
func (s *Session) Wait() (Results, error) {
	<-s.done
	return s.results, s.err
}

// Close releases the stream, stopping the session first if it still runs.
func (s *Session) Close() error {
	var err error
	s.closed.Do(func() {
		s.Stop()
		err = s.stream.Close()
	})
	return err
}
