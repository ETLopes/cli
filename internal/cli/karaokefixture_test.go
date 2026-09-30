package cli

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

// The synthetic world of the karaoke tests: a song on disk, a room that turns
// the played backing into an echo, and a singer who follows the reference.

const (
	fxRate      = 48000
	fxSeconds   = 4.6
	fxNoteLen   = 0.5
	fxNotePitch = 0.75
	fxNoteStart = 0.3
	fxRoundTrip = 120 // samples from playback to the mic
)

var fxMelody = []int{55, 59, 62, 60, 64, 67, 62, 57, 55, 60, 64, 59, 62, 67, 64, 60, 57, 55}

func fxNotes() int { return min(len(fxMelody), int(math.Floor((fxSeconds-fxNoteStart)/fxNotePitch))) }

func fxMidiAt(t float64) (float64, bool) {
	k := int(math.Floor((t - fxNoteStart) / fxNotePitch))
	if k < 0 || k >= fxNotes() {
		return 0, false
	}
	if t-fxNoteStart-float64(k)*fxNotePitch > fxNoteLen {
		return 0, false
	}
	return float64(fxMelody[k]), true
}

func fxHz(m float64) float64 { return 440 * math.Pow(2, (m-69)/12) }

func fxMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fxSong writes a prepared song directory: the reference contour, a 48 kHz
// stereo instrumental and a manifest. The lyrics are placeholder words.
func fxSong(t testing.TB, dir string) song.Song {
	t.Helper()
	fxMust(t, os.MkdirAll(dir, 0o755))
	c := &reference.Contour{HopSeconds: 0.016, SampleRate: 16000}
	for i := 0; float64(i)*0.016 < fxSeconds; i++ {
		tt := float64(i) * 0.016
		c.Time = append(c.Time, tt)
		if m, ok := fxMidiAt(tt); ok {
			c.Hz, c.Confidence, c.LoudnessDB = append(c.Hz, fxHz(m)), append(c.Confidence, 0.95), append(c.LoudnessDB, -20)
		} else {
			c.Hz, c.Confidence, c.LoudnessDB = append(c.Hz, 0), append(c.Confidence, 0.1), append(c.LoudnessDB, -70)
		}
	}
	fxMust(t, c.Save(filepath.Join(dir, song.ReferenceName)))

	n := int(fxSeconds * fxRate)
	stereo := make([]float32, 2*n)
	chords := [][]float64{{130.8, 196, 262}, {110, 164.8, 220}, {146.8, 220, 293.7}, {98, 196, 247}}
	seg := int(0.6 * fxRate)
	for i := 0; i < n; i++ {
		ch := chords[(i/seg)%len(chords)]
		var v float64
		for _, f := range ch {
			for h := 1; h <= 4; h++ {
				v += math.Sin(2*math.Pi*f*float64(h)*float64(i)/fxRate) / float64(h)
			}
		}
		stereo[2*i], stereo[2*i+1] = float32(0.06*v), float32(0.036*v)
	}
	f, err := os.Create(filepath.Join(dir, song.InstrumentalName))
	fxMust(t, err)
	fxMust(t, audioio.WriteWAV(f, &audioio.Audio{SampleRate: fxRate, Channels: 2, Samples: stereo}, audioio.FormatFloat32))
	fxMust(t, f.Close())

	lrc := "[00:00.30]alpha bravo charlie\n[00:03.30]delta echo foxtrot\n"
	fxMust(t, os.WriteFile(filepath.Join(dir, song.SyncedLyricsName), []byte(lrc), 0o644))
	m := song.Manifest{Version: song.ManifestVersion, VideoID: "synthetic", Title: "Synthetic Placeholder",
		Artist: "Placeholder Artist", Track: "Placeholder Track", DurationMS: int64(fxSeconds * 1000), Lyrics: song.LyricsSynced,
		Stages: map[song.Stage]song.StageRecord{
			song.StageRender:    {Artifacts: []string{song.InstrumentalName}},
			song.StageReference: {Artifacts: []string{song.ReferenceName}},
		}}
	data, err := json.Marshal(m)
	fxMust(t, err)
	fxMust(t, os.WriteFile(filepath.Join(dir, song.ManifestName), data, 0o644))
	sg, err := song.Load(dir)
	fxMust(t, err)
	return sg
}

// fxRoom is one microphone: the backing through a few reflections, and, while
// singing is set, a singer following the reference on top.
type fxRoom struct {
	sings    bool
	singing  *atomic.Bool
	hist     []float32
	amp, ph  float64
	echoTaps [][2]float64 // delay in samples, gain
}

func (r *fxRoom) capture(dst []float32, first int64, ref []float32) {
	if first == 0 { // a new stream: forget the last one's playback
		r.hist, r.amp, r.ph = r.hist[:0], 0, 0
	}
	r.hist = append(r.hist, ref...)
	for i := range dst {
		g := int(first) + i
		var v float64
		for _, tap := range r.echoTaps {
			if k := g - int(tap[0]); k >= 0 && k < len(r.hist) {
				v += tap[1] * float64(r.hist[k])
			}
		}
		if r.sings && r.singing.Load() {
			tt := float64(g-fxRoundTrip-2400) / fxRate // 50 ms of singer latency
			target, f0 := 0.0, 0.0
			if m, ok := fxMidiAt(tt); ok && tt >= 0 {
				target, f0 = 0.15, fxHz(m)
			}
			r.amp += (target - r.amp) * 0.01
			if f0 > 0 {
				r.ph = math.Mod(r.ph+2*math.Pi*f0/fxRate, 2*math.Pi)
			}
			if r.amp > 1e-5 {
				var voice float64
				for h := 1; h <= 6; h++ {
					voice += math.Sin(float64(h)*r.ph) / float64(h)
				}
				v += r.amp * voice
			}
		}
		dst[i] = float32(v + 1e-4*math.Sin(float64(g)*1.7))
	}
}

// fxBackend is a four-in, four-out interface with mic 1 sung into (once the
// returned flag is set) and mic 2 hearing only the backing.
func fxBackend() (*audioio.Fake, *atomic.Bool) {
	singing := &atomic.Bool{}
	taps := [][2]float64{{fxRoundTrip, 0.5}, {180, -0.2}, {400, 0.12}, {900, 0.06}}
	m1 := &fxRoom{sings: true, singing: singing, echoTaps: taps}
	m2 := &fxRoom{singing: singing, echoTaps: taps}
	return &audioio.Fake{
		DeviceList: []audioio.Device{{ID: "fake-1", Name: "Fake Interface", CaptureChannels: 4, PlaybackChannels: 4,
			DefaultRate: fxRate, IsDefault: true}},
		Script: []audioio.CaptureFunc{m1.capture, m2.capture},
	}, singing
}

// fxStep is the pump for a fake stream.
func fxStep(ctx context.Context, s audioio.Stream, frames int) error {
	s.(*audioio.FakeStream).Step(frames)
	return ctx.Err()
}

// fxApp builds a karaokeApp over the fake backend with everything in temp dirs.
func fxApp(t testing.TB) (*karaokeApp, *atomic.Bool) {
	t.Helper()
	dir := t.TempDir()
	be, singing := fxBackend()
	return &karaokeApp{
		cfg: config.Karaoke{
			Outputs:    []int{1, 2},
			Inputs:     []config.KaraokeInput{{Channel: 1, Name: "Ana"}, {Channel: 2, Name: "Bruno"}},
			Difficulty: "medium", SlackMS: 100,
			SongsDir: filepath.Join(dir, "songs"), ExportDir: filepath.Join(dir, "export"),
			PausePrepWhileSinging: true,
		},
		queuePath:      filepath.Join(dir, "state", "queue.json"),
		calibrationDir: filepath.Join(dir, "state", "calibrations"),
		backend:        be,
		version:        "test",
		pump:           fxStep,
	}, singing
}
