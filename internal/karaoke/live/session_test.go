package live

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/score"
)

// testSeconds keeps a song long enough for a dozen judged notes and short
// enough that the suite stays fast under -race.
const testSeconds = 6.5

func run(t *testing.T, rs runSpec) (Results, *Session) {
	t.Helper()
	s, _ := start(t, rs)
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return res, s
}

var voice = &singer{latency: 0.05, level: 0.15}

func TestBackingTrackAloneScoresNearZeroAndASingerFollowingTheReferenceScoresHigh(t *testing.T) {
	fx := newFixture(t, testSeconds)
	res, _ := run(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA, &roomB), rooms: []mic{
		{room: roomA, singer: voice},
		{room: roomB}, // nobody singing: the backing bleed only
	}})
	if res.Incomplete || len(res.Players) != 2 {
		t.Fatalf("results = %+v", res)
	}
	t.Logf("singer %d, backing only %d", res.Players[0].Score, res.Players[1].Score)
	if got := res.Players[0].Score; got < 8500 {
		t.Errorf("the singer following the reference scored %d, want at least 8500", got)
	}
	if got := res.Players[1].Score; got > 300 {
		t.Errorf("the backing track alone scored %d, want at most 300", got)
	}
	if res.Players[0].Name != "Player 1" || res.Players[1].Name != "Player 2" || res.Players[1].Channel != 2 {
		t.Errorf("players = %+v", res.Players)
	}
}

func TestASingerACalibratedRoundTripLateIsNotScoredAsInTime(t *testing.T) {
	// The time mapping subtracts the calibrated round trip. A singer half a
	// second behind that is outside the +-100 ms slack and must not score.
	fx := newFixture(t, testSeconds)
	res, _ := run(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA),
		rooms: []mic{{room: roomA, singer: &singer{latency: 0.5, level: 0.15}}}})
	t.Logf("late singer %d", res.Players[0].Score)
	// Notes repeat and octaves fold, so a late singer still lands some hits by
	// chance; in time it is 10000.
	if got := res.Players[0].Score; got > 5000 {
		t.Errorf("a singer 0.5 s late scored %d, want under 5000", got)
	}
}

func TestASingerAnOctaveBelowTheReferenceScoresHigh(t *testing.T) {
	fx := newFixture(t, testSeconds)
	res, _ := run(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA),
		rooms: []mic{{room: roomA, singer: &singer{semitones: -12, latency: 0.05, level: 0.15}}}})
	t.Logf("octave down %d", res.Players[0].Score)
	if got := res.Players[0].Score; got < 8500 {
		t.Errorf("an octave down scored %d, want at least 8500", got)
	}
}

func TestTwoSingersGetIndependentScoresThatMatchTheirBatchScores(t *testing.T) {
	fx := newFixture(t, testSeconds)
	var mu sync.Mutex
	taps := [2][]score.Frame{}
	res, _ := run(t, runSpec{fx: fx, rate: 48000, difficulty: score.Hard,
		cal: knownCalibration(t, 48000, &roomA, &roomB),
		rooms: []mic{
			{room: roomA, singer: voice},
			{room: roomB, singer: &singer{semitones: 2, latency: 0.05, level: 0.15}},
		},
		tap: func(lane int, f score.Frame) { mu.Lock(); taps[lane] = append(taps[lane], f); mu.Unlock() },
	})
	a, b := res.Players[0].Score, res.Players[1].Score
	t.Logf("accurate %d, two semitones off on Hard %d", a, b)
	if a < 8500 {
		t.Errorf("accurate singer scored %d, want at least 8500", a)
	}
	if b > 1500 {
		t.Errorf("singer +2 semitones off on Hard scored %d, want under 1500", b)
	}

	grid, err := loadGrid(fx.song)
	must(t, err)
	l, _, err := fx.song.LoadLyrics()
	must(t, err)
	for i := range taps {
		batch := score.New(grid, l.Lines, score.Config{Difficulty: score.Hard})
		batch.Push(taps[i]...)
		if want := batch.Finish(); want.Score != res.Players[i].Score {
			t.Errorf("player %d: live %d, batch %d", i+1, res.Players[i].Score, want.Score)
		}
	}
}

func TestACaptureRingOverflowIsCountedAndTheSessionGoesOn(t *testing.T) {
	fx := newFixture(t, testSeconds)
	done := false
	s, _ := start(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA),
		rooms: []mic{{room: roomA, singer: voice}},
		hook: func(_ context.Context, _ *Session, _ *world, frame int64) (int, error) {
			if !done && frame >= 2*48000 {
				done = true
				return 4 * 48000, nil // more than a ring holds, in one go
			}
			return 0, nil
		}})
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	t.Logf("overflows: capture %d, reference %d; score %d", snap.Stream.CaptureOverflows, snap.Stream.ReferenceOverflows, res.Players[0].Score)
	if snap.Stream.CaptureOverflows == 0 || snap.Stream.ReferenceOverflows == 0 {
		t.Errorf("stream stats = %+v, want overflows counted", snap.Stream)
	}
	if !snap.Done || res.Incomplete {
		t.Errorf("done %v, incomplete %v: the session should have gone on to the end", snap.Done, res.Incomplete)
	}
	warned := false
	for _, w := range snap.Warnings {
		warned = warned || strings.Contains(w, "dropped")
	}
	if !warned {
		t.Errorf("warnings = %q, want one about dropped audio", snap.Warnings)
	}
	if res.Players[0].Score == 0 {
		t.Error("scoring stopped after the overflow")
	}
}

func TestPausingForTwoSecondsIgnoresThePausedSpan(t *testing.T) {
	fx := newFixture(t, testSeconds)
	base, _ := run(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA), rooms: []mic{{room: roomA, singer: voice}}})

	const pauseAt, resumeAt = 2*48000 + 480, 4*48000 + 480
	var positions []time.Duration
	var pausedFlag []bool
	state := 0
	res, s := run(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA), rooms: []mic{{room: roomA, singer: voice}},
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
			case state == 1 && frame >= pauseAt+48000:
				snap := s.Snapshot()
				positions, pausedFlag = append(positions, snap.Position), append(pausedFlag, snap.Paused)
			}
			return 0, nil
		}})
	got, want := float64(res.Players[0].Score), float64(base.Players[0].Score)
	t.Logf("no pause %d, 2 s pause %d", base.Players[0].Score, res.Players[0].Score)
	if math.Abs(got-want) > 0.01*want {
		t.Errorf("score with a pause %v, without %v: differ by more than 1%%", got, want)
	}
	if len(positions) == 0 || !pausedFlag[0] || positions[0] != positions[len(positions)-1] {
		t.Errorf("while paused: positions %v, paused %v; want a frozen position and Paused set", positions, pausedFlag)
	}
	if snap := s.Snapshot(); snap.Paused || !snap.Done {
		t.Errorf("after the song: paused %v, done %v", snap.Paused, snap.Done)
	}
}

func TestStoppingMidSongGivesIncompleteResultsWithAPartialScore(t *testing.T) {
	fx := newFixture(t, testSeconds)
	reached := make(chan struct{})
	var once sync.Once
	s, _ := start(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA), rooms: []mic{{room: roomA, singer: voice}},
		hook: func(ctx context.Context, _ *Session, _ *world, frame int64) (int, error) {
			if frame >= 3*48000 {
				once.Do(func() { close(reached) })
				<-ctx.Done()
				return 0, ctx.Err()
			}
			return 0, nil
		}})
	<-reached
	res, err := s.Stop()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stopped at 3 s: score %d", res.Players[0].Score)
	if !res.Incomplete || !res.Players[0].Incomplete {
		t.Errorf("incomplete = %v / %v, want both set", res.Incomplete, res.Players[0].Incomplete)
	}
	if got := res.Players[0].Score; got <= 0 || got >= 8500 {
		t.Errorf("partial score %d, want between 0 and the full-song score", got)
	}
	if again, _ := s.Stop(); again.Players[0].Score != res.Players[0].Score {
		t.Error("a second Stop returned different results")
	}
}

func TestA44100HzDeviceUsesTheRendererOnceAndStillScores(t *testing.T) {
	fx := newFixture(t, testSeconds)
	r := &monoRenderer{seconds: testSeconds}
	res, _ := run(t, runSpec{fx: fx, rate: 44100, renderer: r, cal: knownCalibration(t, 44100, &roomA),
		rooms: []mic{{room: roomA, singer: voice}}})
	t.Logf("44.1 kHz score %d", res.Players[0].Score)
	if got := res.Players[0].Score; got < 8500 {
		t.Errorf("score at 44.1 kHz = %d, want at least 8500", got)
	}
	if r.calls != 1 {
		t.Errorf("renderer called %d times, want 1", r.calls)
	}
	if _, err := os.Stat(filepath.Join(fx.song.Dir, "instrumental-mono-44100.wav")); err != nil {
		t.Errorf("the mono render was not cached in the song dir: %v", err)
	}
	// A second session at the same rate reuses the cached file.
	run(t, runSpec{fx: fx, rate: 44100, renderer: r, cal: knownCalibration(t, 44100, &roomA), rooms: []mic{{room: roomA}}})
	if r.calls != 1 {
		t.Errorf("renderer called %d times after a second session, want the cache to be used", r.calls)
	}
}

func TestAMicWithNoEchoPathWarnsAndStillScoresItsPlayer(t *testing.T) {
	fx := newFixture(t, testSeconds)
	dead := roomSpec{delay: 0.01, rt60: 0.2, gain: 0, seed: 3} // nothing of the speakers reaches this mic
	s, _ := start(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA, nil), rooms: []mic{
		{room: roomA, singer: voice}, {room: dead, singer: voice},
	}})
	if w := s.Snapshot().Warnings; len(w) != 1 || !strings.Contains(w[0], "no echo path") || !strings.Contains(w[0], "Player 2") {
		t.Errorf("warnings = %q, want one no-echo-path warning naming Player 2", w)
	}
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scores %d / %d", res.Players[0].Score, res.Players[1].Score)
	if got := res.Players[1].Score; got < 8000 {
		t.Errorf("the player on the unseeded mic scored %d, want at least 8000", got)
	}
}

func TestACalibrationRunOnTheFakeRoomThenSinging(t *testing.T) {
	fx := newFixture(t, testSeconds)
	w := &world{}
	be := fakeBackend(w, 48000, fx.notes, mic{room: roomA}, mic{room: roomB})
	cal, err := calibrate.Run(context.Background(), be, calibrate.Config{
		Stream: audioio.StreamConfig{DeviceName: "Fake Interface", Inputs: []int{1, 2}, Outputs: [2]int{1, 2}},
		LeadIn: 500 * time.Millisecond, SweepDuration: 2 * time.Second, NoiseDuration: 3 * time.Second,
		Pump: func(_ context.Context, s audioio.Stream, frames int) error {
			s.(*audioio.FakeStream).Step(frames)
			return nil
		},
	})
	must(t, err)
	res, _ := run(t, runSpec{fx: fx, rate: 48000, cal: cal, rooms: []mic{{room: roomA, singer: voice}, {room: roomB}}})
	t.Logf("calibrated end to end: singer %d, backing only %d", res.Players[0].Score, res.Players[1].Score)
	if res.Players[0].Score < 8500 || res.Players[1].Score > 300 {
		t.Errorf("scores %d / %d, want at least 8500 and at most 300", res.Players[0].Score, res.Players[1].Score)
	}
}

func TestSnapshotReportsTheSongLineAndPlayersWhileRunningAndIsRaceFree(t *testing.T) {
	fx := newFixture(t, testSeconds)
	s, _ := start(t, runSpec{fx: fx, rate: 48000, cal: knownCalibration(t, 48000, &roomA), rooms: []mic{{room: roomA, singer: voice}}})

	var stop atomic.Bool
	var wg sync.WaitGroup
	var last time.Duration
	var sawLine, sawVoiced, regressed bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			snap := s.Snapshot()
			if snap.Position < last {
				regressed = true
			}
			last = snap.Position
			if snap.LineIndex >= 0 && snap.LineProgress >= 0 && snap.LineProgress <= 1 {
				sawLine = true
			}
			if len(snap.Players) == 1 && snap.Players[0].Voiced && snap.Players[0].TargetVoiced {
				sawVoiced = true
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if _, err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	stop.Store(true)
	wg.Wait()
	snap := s.Snapshot()
	if regressed {
		t.Error("the song position went backwards")
	}
	if !sawLine || !sawVoiced {
		t.Errorf("saw a lyric line: %v, a voiced singer on a voiced target: %v", sawLine, sawVoiced)
	}
	if d := snap.Duration - time.Duration(testSeconds*float64(time.Second)); d < -time.Millisecond || d > time.Millisecond {
		t.Errorf("duration = %v", snap.Duration)
	}
	if snap.Players[0].Score <= 0 || snap.Stream.Frames == 0 || snap.Players[0].LevelDBFS > 0 || snap.Players[0].LevelDBFS < -120 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestAProcessingIterationAllocatesNothingOnceWarm(t *testing.T) {
	fx := newFixture(t, testSeconds)
	w := &world{}
	be := fakeBackend(w, 48000, fx.notes, mic{room: roomA, singer: voice}, mic{room: roomB})
	cfg := SessionConfig{Song: fx.song, Calibration: knownCalibration(t, 48000, &roomA, &roomB), Backend: be,
		Stream:  audioio.StreamConfig{DeviceName: "Fake Interface", Inputs: []int{1, 2}},
		Players: []string{"a", "b"},
		Pump: func(_ context.Context, st audioio.Stream, n int) error {
			st.(*audioio.FakeStream).Step(n)
			return nil
		}}
	s, err := newSession(context.Background(), cfg)
	must(t, err)
	must(t, s.stream.Start())
	defer s.stream.Close() // no goroutine runs here, so not Session.Close
	ctx := context.Background()
	for range 50 { // 1 s: the buffers and the chains reach their final size
		if _, err := s.iterate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	withScorer := testing.AllocsPerRun(20, func() { s.iterate(ctx) })
	// score.Scorer.Push allocates (it lives in another package), so the
	// claim is about this package's processing: everything but the scorer.
	s.pipe.noScore = true
	got := testing.AllocsPerRun(50, func() { s.iterate(ctx) })
	t.Logf("allocations per iteration: %v without the scorer, %v with it", got, withScorer)
	if got != 0 {
		t.Errorf("an iteration allocated %v times, want 0", got)
	}
}
