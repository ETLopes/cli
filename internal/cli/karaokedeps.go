package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/karaoke/song"
	"github.com/ETLopes/cli/internal/karaoke/ultrastar"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/youtube"
)

// prepNice is how much the preparation tools yield to the audio: live karaoke
// has a hard real-time deadline and Demucs saturates the CPU.
const prepNice = 10

// errNotCalibrated says a session cannot start because the setup has no
// calibration yet.
var errNotCalibrated = errors.New("no calibration for this device, rate and output pair")

// karaokeApp is everything the karaoke commands, and the TUI built on them,
// need: the settings, where things live, and the collaborators that touch the
// outside world. Tests build one around a fake backend and a fake runner.
type karaokeApp struct {
	cfg config.Karaoke
	// dtx carries the separation model, compute device and cookies, which the
	// karaoke preparation shares with the dtx tool.
	dtx config.Config

	// queuePath and calibrationDir are the state, under the data dir.
	queuePath      string
	calibrationDir string

	backend audioio.Backend
	// run is the runner the preparation tools use, already niced.
	run runner.Runner
	// lyricsURL overrides LRCLIB's address; tests point it at a local server.
	lyricsURL string
	version   string
	// demucsPath is the resolved Demucs executable, set once the tools exist.
	demucsPath string
	// python overrides the managed environment's interpreter for swift-f0.
	python string

	// pump nil sleeps, which is right for a real device; a test steps the fake
	// stream instead.
	pump live.Pump
	now  func() time.Time
}

// newKaraokeApp builds the production wiring from the resolved settings.
func newKaraokeApp(e *env) *karaokeApp {
	return &karaokeApp{
		cfg:            e.karaoke,
		dtx:            e.cfg,
		queuePath:      config.KaraokeQueuePath(),
		calibrationDir: config.KaraokeCalibrationDir(),
		backend:        audioio.Default(),
		run:            runner.Nice(runner.New(), prepNice),
		version:        version,
		now:            time.Now,
	}
}

func (a *karaokeApp) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// OpenQueue opens the persisted queue, creating its directory on first use.
// The warning is set when an unreadable queue file was moved aside.
func (a *karaokeApp) OpenQueue() (*queue.Store, *queue.Warning, error) {
	if err := os.MkdirAll(filepath.Dir(a.queuePath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating the karaoke state directory: %w", err)
	}
	return queue.Open(a.queuePath)
}

// Preparer builds the pipeline that turns a URL into a song directory.
func (a *karaokeApp) Preparer() *song.Preparer {
	sep := separate.New(a.run)
	sep.Path = a.demucsPath
	yt := youtube.New(a.run)
	yt.CookiesFromBrowser = a.dtx.CookiesFromBrowser
	return &song.Preparer{
		YouTube:   yt,
		Separator: sep,
		Audio:     audio.New(a.run),
		Reference: &reference.SwiftF0{Run: a.run, Python: a.python},
		Lyrics:    &lyrics.Client{BaseURL: a.lyricsURL, Version: a.version},
		Model:     a.dtx.Model,
		Device:    a.dtx.Device,
	}
}

// NewWorker returns the background worker that prepares queued songs into the
// song library.
func (a *karaokeApp) NewWorker(store *queue.Store) *queue.Worker {
	return queue.NewWorker(store, a.Preparer(), a.cfg.SongsDir)
}

// Device finds the audio device the settings name, or the system default.
func (a *karaokeApp) Device() (audioio.Device, error) {
	devs, err := a.backend.Devices()
	if err != nil {
		return audioio.Device{}, err
	}
	if a.cfg.Device != "" {
		for _, d := range devs {
			if strings.EqualFold(d.Name, a.cfg.Device) {
				return d, nil
			}
		}
		return audioio.Device{}, fmt.Errorf("%s", i18n.Tf("karaoke.err.no_device", a.cfg.Device))
	}
	for _, d := range devs {
		if d.IsDefault {
			return d, nil
		}
	}
	if len(devs) == 0 {
		return audioio.Device{}, fmt.Errorf("%s", i18n.T("karaoke.err.no_devices"))
	}
	return devs[0], nil
}

// CalibrationStatus is what is known about the calibration for the configured
// device, rate and output pair.
type CalibrationStatus struct {
	Key calibrate.Key
	// Found is false when nothing has been calibrated for Key.
	Found  bool
	Result calibrate.Result
	// Stale means it should be redone; Reason says why.
	Stale  bool
	Reason string
	Age    time.Duration
}

// Calibration loads the stored calibration for the configured setup and checks
// whether it is still trustworthy.
func (a *karaokeApp) Calibration() (CalibrationStatus, error) {
	dev, err := a.Device()
	if err != nil {
		return CalibrationStatus{}, err
	}
	key := calibrate.Key{Device: dev.Name, SampleRate: dev.DefaultRate, Outputs: a.cfg.OutputPair()}
	st := CalibrationStatus{Key: key}
	res, ok, err := calibrate.Load(a.calibrationDir, key)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, nil
	}
	st.Found, st.Result = true, res
	st.Age = max(0, a.clock().Sub(res.Time))
	if calibrate.Stale(res, a.clock(), calibrate.StaleConfig{Inputs: a.cfg.InputChannels()}) {
		st.Stale = true
		if st.Age > calibrate.DefaultMaxAge {
			st.Reason = i18n.Tf("karaoke.stale.age", int(st.Age.Hours()/24))
		} else {
			st.Reason = i18n.T("karaoke.stale.inputs")
		}
	}
	return st, nil
}

// Calibrate plays the sweep through the configured outputs and measures every
// configured microphone. It does not save; see SaveCalibration.
func (a *karaokeApp) Calibrate(ctx context.Context, progress func(calibrate.Stage, float64)) (calibrate.Result, error) {
	dev, err := a.Device()
	if err != nil {
		return calibrate.Result{}, err
	}
	return calibrate.Run(ctx, a.backend, calibrate.Config{
		Stream: audioio.StreamConfig{
			DeviceName: dev.Name,
			Inputs:     a.cfg.InputChannels(),
			Outputs:    a.cfg.OutputPair(),
		},
		Progress: progress,
		Pump:     a.pump,
		Now:      a.now,
	})
}

// SaveCalibration stores a result where Calibration will find it.
func (a *karaokeApp) SaveCalibration(r calibrate.Result) error {
	return calibrate.Save(a.calibrationDir, r)
}

// StartSession starts singing entry's song with the given players, each on the
// microphone their input names. No players means every configured one. It
// refuses to start without a calibration for this setup; an old one only
// produces a warning inside the session.
func (a *karaokeApp) StartSession(ctx context.Context, entry queue.Entry, players []config.KaraokeInput) (*live.Session, error) {
	if entry.SongDir == "" {
		return nil, fmt.Errorf("%s", i18n.Tf("karaoke.err.not_ready", entry.Title))
	}
	sg, err := song.Load(entry.SongDir)
	if err != nil {
		return nil, err
	}
	if !sg.Ready() {
		return nil, fmt.Errorf("%s", i18n.Tf("karaoke.err.not_ready", entry.Title))
	}
	if len(players) == 0 {
		players = a.cfg.Inputs
	}
	cal, err := a.Calibration()
	if err != nil {
		return nil, err
	}
	if !cal.Found {
		return nil, errNotCalibrated
	}
	difficulty, err := score.ParseDifficulty(a.cfg.Difficulty)
	if err != nil {
		return nil, err
	}

	inputs := make([]int, len(players))
	names := make([]string, len(players))
	for i, p := range players {
		inputs[i], names[i] = p.Channel, p.Name
	}
	cfg := live.SessionConfig{
		Song:             sg,
		Calibration:      cal.Result,
		CalibrationStale: cal.Reason,
		Backend:          a.backend,
		Stream: audioio.StreamConfig{
			DeviceName: cal.Key.Device,
			Inputs:     inputs,
			Outputs:    cal.Key.Outputs,
			SampleRate: cal.Key.SampleRate,
		},
		Players:    names,
		Difficulty: difficulty,
		Slack:      time.Duration(a.cfg.SlackMS) * time.Millisecond,
		Pump:       a.pump,
		Renderer:   audio.New(a.run),
	}
	if a.cfg.RecordDir != "" {
		cfg.RecordDir = filepath.Join(a.cfg.RecordDir,
			fmt.Sprintf("%s-%s", a.clock().Format("20060102-150405"), entry.ID))
	}
	return live.Open(ctx, cfg)
}

// Export writes entry's song as an UltraStar folder under outDir, or under the
// configured export folder when outDir is empty.
func (a *karaokeApp) Export(ctx context.Context, entry queue.Entry, outDir string) (ultrastar.Result, error) {
	if entry.SongDir == "" {
		return ultrastar.Result{}, fmt.Errorf("%s", i18n.Tf("karaoke.err.not_ready", entry.Title))
	}
	return a.ExportDir(ctx, entry.SongDir, outDir)
}

// ExportDir is Export for a song directory that is not in the queue.
func (a *karaokeApp) ExportDir(ctx context.Context, songDir, outDir string) (ultrastar.Result, error) {
	sg, err := song.Load(songDir)
	if err != nil {
		return ultrastar.Result{}, err
	}
	if outDir == "" {
		outDir = a.cfg.ExportDir
	}
	return ultrastar.Export(ctx, sg, outDir, audio.New(a.run))
}

// MicLevel is how loud one microphone was over a short recording.
type MicLevel struct {
	Channel int
	Name    string
	// RMSDBFS and PeakDBFS are -Inf for digital silence.
	RMSDBFS, PeakDBFS float64
	// Silent means every sample was exactly zero, which a live microphone never
	// produces: the operating system is withholding the audio.
	Silent bool
}

// MicLevels records d from the configured inputs and reports each one's level.
func (a *karaokeApp) MicLevels(ctx context.Context, d time.Duration) ([]MicLevel, error) {
	dev, err := a.Device()
	if err != nil {
		return nil, err
	}
	stream, err := a.backend.Open(audioio.StreamConfig{
		DeviceName: dev.Name,
		Inputs:     a.cfg.InputChannels(),
		Outputs:    a.cfg.OutputPair(),
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if err := stream.Start(); err != nil {
		return nil, err
	}

	pump := a.pump
	if pump == nil {
		pump = func(ctx context.Context, s audioio.Stream, frames int) error {
			t := time.NewTimer(time.Duration(frames) * time.Second / time.Duration(s.SampleRate()))
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}

	n := len(a.cfg.Inputs)
	sumSq, peak, count := make([]float64, n), make([]float64, n), make([]float64, n)
	buf := make([]float32, 4096)
	step := stream.SampleRate() / 50
	for elapsed := 0; elapsed < int(d/time.Millisecond)/20; elapsed++ {
		if err := pump(ctx, stream, step); err != nil {
			return nil, err
		}
		for i := range n {
			for {
				got := stream.Capture(i).Read(buf)
				if got == 0 {
					break
				}
				for _, v := range buf[:got] {
					f := float64(v)
					sumSq[i] += f * f
					peak[i] = max(peak[i], math.Abs(f))
				}
				count[i] += float64(got)
			}
		}
	}

	out := make([]MicLevel, n)
	for i, in := range a.cfg.Inputs {
		l := MicLevel{Channel: in.Channel, Name: in.Name, RMSDBFS: math.Inf(-1), PeakDBFS: math.Inf(-1), Silent: peak[i] == 0}
		if !l.Silent {
			l.RMSDBFS = 10 * math.Log10(sumSq[i]/count[i])
			l.PeakDBFS = 20 * math.Log10(peak[i])
		}
		out[i] = l
	}
	return out, nil
}
