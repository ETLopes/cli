package live

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/dsp"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

// This file is the synthetic world of the tests: a song on disk, a room that
// turns the played backing into an echo in each microphone, and a singer who
// follows the reference. Everything is deterministic.

// ---- the song --------------------------------------------------------------

const (
	noteLen   = 0.5  // seconds a note is held
	notePitch = 0.75 // seconds from one note to the next
	noteStart = 0.3  // seconds before the first note
)

// melody is held notes between 196 and 392 Hz with gaps.
var melody = []int{55, 59, 62, 60, 64, 67, 62, 57, 55, 60, 64, 59, 62, 67, 64, 60, 57, 55}

// midiAt is the note sung at song time t seconds.
func midiAt(t float64, n int) (float64, bool) {
	k := int(math.Floor((t - noteStart) / notePitch))
	if k < 0 || k >= n || k >= len(melody) {
		return 0, false
	}
	if t-noteStart-float64(k)*notePitch > noteLen {
		return 0, false
	}
	return float64(melody[k]), true
}

func midiHz(m float64) float64 { return 440 * math.Pow(2, (m-69)/12) }

// fixture is a prepared song in a temp dir.
type fixture struct {
	song    song.Song
	seconds float64
	notes   int
}

// music is the backing: chords that change every 0.6 s, with partials that sit
// on the melody's notes, over a soft noise bed. It is mono at the given rate.
func music(n, rate int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, n)
	chords := [][]float64{{130.8, 196, 262}, {110, 164.8, 220}, {146.8, 220, 293.7}, {98, 196, 247}}
	seg := int(0.6 * float64(rate))
	for pos := 0; pos < n; pos += seg {
		ch := chords[rng.Intn(len(chords))]
		for i := 0; i < seg && pos+i < n; i++ {
			t := float64(pos+i) / float64(rate)
			var v float64
			for _, f := range ch {
				for h := 1; h <= 4; h++ {
					v += math.Sin(2*math.Pi*f*float64(h)*t) / float64(h)
				}
			}
			out[pos+i] = float32(0.06*v + 0.02*rng.NormFloat64())
		}
	}
	return out
}

func newFixture(t testing.TB, seconds float64) fixture {
	t.Helper()
	dir := t.TempDir()
	notes := min(len(melody), int((seconds-noteStart)/notePitch))

	c := &reference.Contour{HopSeconds: 0.016, SampleRate: 16000}
	for i := 0; float64(i)*0.016 < seconds; i++ {
		tt := float64(i) * 0.016
		c.Time = append(c.Time, tt)
		if m, ok := midiAt(tt, notes); ok {
			c.Hz, c.Confidence, c.LoudnessDB = append(c.Hz, midiHz(m)), append(c.Confidence, 0.95), append(c.LoudnessDB, -20)
		} else {
			c.Hz, c.Confidence, c.LoudnessDB = append(c.Hz, 0), append(c.Confidence, 0.1), append(c.LoudnessDB, -70)
		}
	}
	must(t, c.Save(filepath.Join(dir, song.ReferenceName)))

	n := int(seconds * instrumentalRate)
	left := music(n, instrumentalRate, 1)
	stereo := make([]float32, 2*n)
	for i, v := range left {
		stereo[2*i], stereo[2*i+1] = v, 0.6*v
	}
	writeWAV(t, filepath.Join(dir, song.InstrumentalName), &audioio.Audio{SampleRate: instrumentalRate, Channels: 2, Samples: stereo})

	lrc := "[ti:Placeholder]\n"
	words := []string{"alpha bravo charlie", "delta echo foxtrot", "golf hotel india", "juliet kilo lima", "mike november oscar"}
	for i := 0; float64(i)*3 < seconds; i++ {
		lrc += fmt.Sprintf("[00:%05.2f]%s\n", float64(i)*3+0.3, words[i%len(words)])
	}
	must(t, os.WriteFile(filepath.Join(dir, song.SyncedLyricsName), []byte(lrc), 0o644))

	m := song.Manifest{Version: song.ManifestVersion, VideoID: "synthetic", Title: "Synthetic", DurationMS: int64(seconds * 1000), Lyrics: song.LyricsSynced}
	data, err := json.Marshal(m)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, song.ManifestName), data, 0o644))
	sg, err := song.Load(dir)
	must(t, err)
	return fixture{song: sg, seconds: seconds, notes: notes}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeWAV(t testing.TB, path string, a *audioio.Audio) {
	t.Helper()
	f, err := os.Create(path)
	must(t, err)
	defer f.Close()
	must(t, audioio.WriteWAV(f, a, audioio.FormatFloat32))
}

// monoRenderer writes the backing as a mono WAV at the requested rate, as
// ffmpeg would, and counts its calls.
type monoRenderer struct {
	seconds float64
	calls   int
}

func (r *monoRenderer) RenderWAV(_ context.Context, _, dst string, spec audio.WAVSpec, _ time.Duration, _ audio.ProgressFunc) error {
	r.calls++
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	return audioio.WriteWAV(f, &audioio.Audio{SampleRate: spec.SampleRate, Channels: 1, Samples: music(int(r.seconds*float64(spec.SampleRate)), spec.SampleRate, 1)}, audioio.FormatFloat32)
}

// ---- the room --------------------------------------------------------------

type roomSpec struct {
	delay float64 // seconds before the direct sound arrives
	rt60  float64 // seconds for the tail to fall by 60 dB
	gain  float64 // amplitude of the direct sound
	seed  int64
}

var (
	roomA = roomSpec{delay: 0.0123, rt60: 0.3, gain: 0.5, seed: 1}
	roomB = roomSpec{delay: 0.0317, rt60: 0.25, gain: 0.4, seed: 2}
)

type impulse struct{ t, a float64 }

// impulses is the room as a list of arrivals: the direct sound, eight early
// reflections and a decaying scatter. As a list it can be rendered at any rate.
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

// render is the response at rate, seconds long; every arrival is a 5 kHz
// low-passed pulse so the room is band-limited like a speaker and a mic.
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

// span is a stretch of stream frames [from, to) during which the song was
// paused; to < 0 means it still is.
type span struct{ from, to int64 }

// world is what a test steers while a session runs: when the song was paused.
type world struct{ pauses []span }

func (w *world) pause(at int64) { w.pauses = append(w.pauses, span{at, -1}) }
func (w *world) resume(at int64) {
	if n := len(w.pauses); n > 0 && w.pauses[n-1].to < 0 {
		w.pauses[n-1].to = at
	}
}

// songFrame is the song position the source played at stream frame f, and
// false while it was paused.
func (w *world) songFrame(f int64) (int64, bool) {
	var paused int64
	for _, p := range w.pauses {
		if f < p.from {
			break
		}
		if p.to < 0 || f < p.to {
			return 0, false
		}
		paused += p.to - p.from
	}
	return f - paused, true
}

// singer is a harmonic voice that follows the reference melody, offset by
// semitones, starting latency seconds after the round trip.
type singer struct {
	semitones float64
	latency   float64
	level     float64
}

// mic scripts one microphone: the backing through the room, a little noise
// and, optionally, a singer.
type mic struct {
	room   roomSpec
	singer *singer
}

func (m mic) capture(w *world, rate, notes int, seed int64) audioio.CaptureFunc {
	rng := rand.New(rand.NewSource(seed))
	conv := newConvolver(render(m.room.impulses(), rate, 0.45), 4096)
	echo := make([]float32, 4096)
	roundTrip := int64(m.room.delay * float64(rate))
	var phase, amp float64
	return func(dst []float32, first int64, ref []float32) {
		conv.process(echo[:len(dst)], ref)
		for i := range dst {
			v := echo[i] + float32(1e-4*rng.NormFloat64())
			if s := m.singer; s != nil {
				g := first + int64(i) - roundTrip - int64(s.latency*float64(rate))
				target := 0.0
				var f0 float64
				if song, ok := w.songFrame(g); ok && g >= 0 {
					if midi, sung := midiAt(float64(song)/float64(rate), notes); sung {
						target, f0 = s.level, midiHz(midi+s.semitones)
					}
				}
				amp += (target - amp) * 0.01
				if f0 > 0 {
					phase += 2 * math.Pi * f0 / float64(rate)
					if phase > 2*math.Pi {
						phase -= 2 * math.Pi
					}
				}
				if amp > 1e-5 {
					var voice float64
					for h := 1; h <= 6; h++ {
						voice += math.Sin(float64(h)*phase) / float64(h)
					}
					v += float32(amp * voice)
				}
			}
			dst[i] = v
		}
	}
}

func fakeBackend(w *world, rate, notes int, mics ...mic) *audioio.Fake {
	f := &audioio.Fake{DeviceList: []audioio.Device{{
		ID: "fake-1", Name: "Fake Interface", CaptureChannels: 4, PlaybackChannels: 4, DefaultRate: rate, IsDefault: true,
	}}}
	for i, m := range mics {
		f.Script = append(f.Script, m.capture(w, rate, notes, int64(100+i)))
	}
	return f
}

// ---- calibration -----------------------------------------------------------

// knownCalibration builds the calibration a perfect measurement of the rooms
// would give, following calibrate's conventions (SeedIR: the response from the
// moment the reference played, less BulkDelay zeros), without running it. The
// residual floor is what a seeded chain leaves of the backing alone.
func knownCalibration(t testing.TB, rate int, rooms ...*roomSpec) calibrate.Result {
	t.Helper()
	res := calibrate.Result{Device: "Fake Interface", SampleRate: rate, Outputs: [2]int{1, 2}, Time: time.Unix(0, 0)}
	for i, r := range rooms {
		in := calibrate.Input{Channel: i + 1, NoiseFloorDBFS: -80}
		if r == nil {
			in.NoEchoPath = true
			res.Inputs = append(res.Inputs, in)
			continue
		}
		truth := render(r.impulses(), dsp.PipelineRate, 0.9)
		delay := r.delay * dsp.PipelineRate
		in.DelaySamples, in.DelayMs = delay, r.delay*1000
		in.BulkDelay = int(math.Floor(delay)) - 8
		end := int(delay) + int(0.35*dsp.PipelineRate)
		in.IR = make([]float32, end-in.BulkDelay)
		for k := range in.IR {
			in.IR[k] = float32(truth[in.BulkDelay+k])
		}
		in.TailMs = 350
		floorMu.Lock()
		floor, ok := floors[*r]
		if !ok { // measuring takes seconds under -race, and rooms repeat
			floor = residualFloor(t, in, truth)
			floors[*r] = floor
		}
		floorMu.Unlock()
		in.ResidualFloorDBFS = 20 * math.Log10(floor)
		res.Inputs = append(res.Inputs, in)
	}
	return res
}

var (
	floorMu sync.Mutex
	floors  = map[roomSpec]float64{}
)

// residualFloor runs a seeded chain over the backing alone at 16 kHz and
// returns the largest cleaned level once it has settled.
func residualFloor(t testing.TB, in calibrate.Input, truth []float64) float64 {
	t.Helper()
	const n = 5 * dsp.PipelineRate
	ref := music(n, dsp.PipelineRate, 9)
	mic := make([]float32, n)
	conv := newConvolver(truth, dsp.CancellerBlock)
	for p := 0; p+dsp.CancellerBlock <= n; p += dsp.CancellerBlock {
		conv.process(mic[p:p+dsp.CancellerBlock], ref[p:p+dsp.CancellerBlock])
	}
	chain, err := dsp.NewChain(dsp.ChainConfig{Tail: len(in.IR), BulkDelay: in.BulkDelay, ImpulseResponse: in.SeedIR()})
	must(t, err)
	var floor float64
	for p := 0; p+dsp.CancellerBlock <= n; p += dsp.CancellerBlock {
		if f, ok := chain.Process(ref[p:p+dsp.CancellerBlock], mic[p:p+dsp.CancellerBlock]); ok && f.Index*dsp.GateHop >= dsp.PipelineRate {
			floor = math.Max(floor, f.Level)
		}
	}
	return floor
}

// ---- running a session -----------------------------------------------------

// stepHook runs before every pump step with the stream frame reached and
// returns how many frames to advance (0 means the default step).
type stepHook func(ctx context.Context, s *Session, w *world, frame int64) (int, error)

type runSpec struct {
	fx         fixture
	rate       int
	rooms      []mic
	cal        calibrate.Result
	difficulty score.Difficulty
	hook       stepHook
	recordDir  string
	renderer   Renderer
	// tap, if set, sees every frame handed to a scorer, by lane.
	tap     func(lane int, f score.Frame)
	players []string
}

// start builds a session over a fake backend, wired so that hook can steer
// the world and the session, and starts it.
func start(t testing.TB, rs runSpec) (*Session, *world) {
	t.Helper()
	w := &world{}
	be := fakeBackend(w, rs.rate, rs.fx.notes, rs.rooms...)
	inputs := make([]int, len(rs.rooms))
	for i := range inputs {
		inputs[i] = i + 1
	}
	players := rs.players
	if players == nil {
		for i := range inputs {
			players = append(players, fmt.Sprintf("Player %d", i+1))
		}
	}
	var s *Session
	pump := func(ctx context.Context, st audioio.Stream, n int) error {
		if rs.hook != nil {
			frame := int64(st.Stats().Frames.Load())
			if k, err := rs.hook(ctx, s, w, frame); err != nil {
				return err
			} else if k > 0 {
				n = k
			}
		}
		st.(*audioio.FakeStream).Step(n)
		return ctx.Err()
	}
	cfg := SessionConfig{
		Song: rs.fx.song, Calibration: rs.cal, Backend: be,
		Stream:  audioio.StreamConfig{DeviceName: "Fake Interface", Inputs: inputs},
		Players: players, Difficulty: rs.difficulty, RecordDir: rs.recordDir,
		Pump: pump, Renderer: rs.renderer,
	}
	s, err := newSession(context.Background(), cfg)
	must(t, err)
	s.pipe.tap = rs.tap
	must(t, s.stream.Start())
	var ctx context.Context
	ctx, s.cancel = context.WithCancel(context.Background())
	go s.run(ctx)
	t.Cleanup(func() { s.Close() })
	return s, w
}
