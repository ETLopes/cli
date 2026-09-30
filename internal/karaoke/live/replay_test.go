package live

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
)

func TestReplayOfARecordedSessionGivesTheSameScoresAndSensibleMetrics(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, testSeconds)
	dir := filepath.Join(t.TempDir(), "bundle")
	cal := knownCalibration(t, 48000, &roomA, &roomB)

	// A pause and a ring overflow, so the replay has both to reproduce.
	const pauseAt, resumeAt, overflowAt = 1 * 48000, 1*48000 + 24000, 4 * 48000
	state, flooded := 0, false
	live, s := run(t, runSpec{fx: fx, rate: 48000, cal: cal, recordDir: dir,
		rooms: []mic{{room: roomA, singer: voice}, {room: roomB}},
		hook: func(_ context.Context, s *Session, w *world, frame int64) (int, error) {
			switch {
			case state == 0 && frame >= pauseAt:
				s.Pause()
				w.pause(frame)
				state = 1
			case state == 1 && frame >= resumeAt:
				s.Resume()
				w.resume(frame)
				state = 2
			case !flooded && frame >= overflowAt:
				flooded = true
				return 4 * 48000, nil
			}
			return 0, nil
		}})
	if w := s.Snapshot().Warnings; len(w) == 0 {
		t.Error("no warning about the overflow")
	}

	b, err := ReadBundle(dir)
	must(t, err)
	if b.SampleRate != 48000 || !reflect.DeepEqual(b.Inputs, []int{1, 2}) || !reflect.DeepEqual(b.Players, []string{"Player 1", "Player 2"}) ||
		b.Difficulty != "easy" || b.SongDir != fx.song.Dir || b.Incomplete || len(b.Gaps) != 1 || b.Calibration.Device != "Fake Interface" {
		t.Errorf("bundle = %+v", b)
	}
	if len(b.Segments) != 3 || !b.Segments[1].Paused || b.Segments[1].Stream != pauseAt || b.Segments[2].Paused || b.Segments[2].Stream != resumeAt || b.Segments[2].Song != b.Segments[1].Song {
		t.Errorf("segments = %+v, want play, pause at %d, resume at %d from the same song frame", b.Segments, pauseAt, resumeAt)
	}
	for _, name := range []string{"reference.wav", "input-1.wav", "input-2.wav"} {
		a, err := audioio.ReadWAVFile(filepath.Join(dir, name))
		must(t, err)
		if a.Channels != 1 || a.SampleRate != 48000 || int64(a.Frames()) != b.Frames {
			t.Errorf("%s: %d ch, %d Hz, %d frames; manifest says %d", name, a.Channels, a.SampleRate, a.Frames(), b.Frames)
		}
	}

	replayed, metrics, err := Replay(context.Background(), dir, cal, fx.song)
	must(t, err)
	for i := range live.Players {
		if !reflect.DeepEqual(live.Players[i], replayed.Players[i]) {
			t.Errorf("player %d: live %+v, replay %+v", i+1, live.Players[i], replayed.Players[i])
		}
	}
	t.Logf("scores %d / %d; metrics %+v", replayed.Players[0].Score, replayed.Players[1].Score, metrics.Mics)

	m0, m1 := metrics.Mics[0], metrics.Mics[1]
	if m0.VoicedFraction < 0.2 || m0.VoicedFraction > 0.8 {
		t.Errorf("singing mic voiced fraction %.2f, want a sung song's share", m0.VoicedFraction)
	}
	if m1.VoicedFraction > 0.02 {
		t.Errorf("backing-only mic voiced fraction %.3f, want about 0", m1.VoicedFraction)
	}
	if m1.EchoFrames < 100 || m1.EchoReductionDB < 20 {
		t.Errorf("backing-only mic: %d echo frames, reduction %.1f dB; want many frames and at least 20 dB", m1.EchoFrames, m1.EchoReductionDB)
	}
	if m1.MeanInputDBFS > -10 || m1.MeanInputDBFS < -60 || m1.Channel != 2 || m1.Name != "Player 2" {
		t.Errorf("backing-only mic metrics = %+v", m1)
	}
}

func TestRecordingThatCannotBeCreatedWarnsAndTheSessionIsUnaffected(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, testSeconds)
	blocker := filepath.Join(t.TempDir(), "file")
	must(t, os.WriteFile(blocker, nil, 0o644))
	s, _ := start(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA), recordDir: filepath.Join(blocker, "bundle"),
		rooms: []mic{{room: roomA, singer: voice}}})
	if w := s.Snapshot().Warnings; len(w) != 1 || !strings.Contains(w[0], "recording disabled") {
		t.Errorf("warnings = %q", w)
	}
	s.Stop()
}

func TestADiskErrorWhileRecordingStopsTheRecordingWithoutPanicking(t *testing.T) {
	t.Parallel()
	r, err := newRecorder(filepath.Join(t.TempDir(), "b"), 48000, []int{1})
	must(t, err)
	x := make([]float32, chunkFrames)
	r.write(x, [][]float32{x})
	// Break the disk under the recorder: the next flush fails.
	r.caps[0].f.Close()
	for range 100 {
		r.write(x, [][]float32{x})
	}
	if r.failure() == nil {
		t.Fatal("no failure recorded after the file was closed under the recorder")
	}
	r.write(x, [][]float32{x}) // no-op after a failure
	r.finish(Bundle{})
	if _, err := os.Stat(filepath.Join(r.dir, bundleManifest)); err == nil {
		t.Error("a manifest was written for a broken recording")
	}
	var nilRec *recorder
	nilRec.write(x, [][]float32{x}) // a session without recording calls the same methods
	nilRec.gap(1)
	nilRec.finish(Bundle{})
	if nilRec.failure() != nil {
		t.Error("nil recorder reports a failure")
	}
}
