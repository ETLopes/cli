package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/queue"
)

// The whole path through the screen with the real store, worker, preparer and
// live session: only the outside world is fake (yt-dlp, demucs and friends,
// and the audio device with a room and a scripted singer).
func TestAddPrepareSingAndResultsThroughTheRealParts(t *testing.T) {
	app, _, singing := prepAppSinging(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The room was silent while it was measured.
	cal, err := app.Calibrate(ctx, nil)
	fxMust(t, err)
	fxMust(t, app.SaveCalibration(cal))

	store, _, err := app.OpenQueue()
	fxMust(t, err)
	worker := app.NewWorker(store)
	go func() { _ = worker.Run(ctx) }()

	// The events are read here, not by the model, so the test controls when
	// each one is applied.
	m := newKaraokeModel(ctx, appHost{app}, store, worker, nil, app.cfg)
	m = ksend(m, msgOf(m.loadCalibration()))
	if !strings.Contains(kscreen(m), "Calibration ok") {
		t.Fatalf("header should report the calibration:\n%s", kscreen(m))
	}

	// Add.
	m = kpress(ktype(m, kTestURL), "enter")
	if n := len(store.Entries()); n != 1 {
		t.Fatalf("queue has %d entries after adding, want 1", n)
	}

	// Prepare: apply the worker's events as they come until the song is ready.
	deadline := time.After(20 * time.Second)
	for ready := false; !ready; {
		select {
		case ev := <-worker.Events():
			m = ksend(m, workerEventMsg{ev})
			ready = ev.Kind == queue.EventState && ev.State == queue.Ready
		case <-deadline:
			t.Fatalf("the song was never prepared:\n%s", kscreen(m))
		}
	}
	entry := store.Entries()[0]
	if !strings.Contains(kscreen(m), kTestTitle) {
		t.Errorf("the ready row should carry the title:\n%s", kscreen(m))
	}
	// The fake tools write placeholder files; put a real synthetic song where
	// the preparer left its directory, as the real tools would have.
	fxSong(t, entry.SongDir)

	// Sing.
	singing.Store(true)
	m = m.focusList()
	m, cmd := ksendCmd(m, tuiKey("enter"))
	if m.screen != screenSing {
		t.Fatalf("screen = %d, notice %q", m.screen, m.notice)
	}
	for i := 0; m.screen == screenSing && i < 5000; i++ {
		msg := msgWithin(cmd, 20*time.Second)
		if msg == nil {
			t.Fatalf("the sing view stalled:\n%s", kscreen(m))
		}
		m, cmd = ksendCmd(m, msg)
	}

	// Results.
	if m.screen != screenResults {
		t.Fatalf("screen = %d, want results; notice %q", m.screen, m.notice)
	}
	got, _ := store.Get(entry.ID)
	if got.State != queue.Sung || len(got.Scores) != 1 {
		t.Fatalf("entry = %+v, want sung with one score", got)
	}
	sc := got.Scores[0]
	if !sc.Completed || len(sc.Players) != 2 || sc.Players[0].Name != "Ana" {
		t.Fatalf("score = %+v", sc)
	}
	t.Logf("Ana %d, Bruno (echo only) %d", sc.Players[0].Score, sc.Players[1].Score)
	if sc.Players[0].Score < 8000 {
		t.Errorf("the scripted singer scored %d, want at least 8000", sc.Players[0].Score)
	}
	if sc.Players[1].Score > 400 {
		t.Errorf("the echo-only input scored %d, want near zero", sc.Players[1].Score)
	}
	m = kpress(m, "enter") // skip the reveal
	if out := kscreen(m); !strings.Contains(out, "Ana ★") || !strings.Contains(out, "Winner: Ana") {
		t.Errorf("results should crown Ana:\n%s", out)
	}

	// Back to the queue, with the score on the row.
	m = kpress(m, "enter")
	if out := kscreen(m); m.screen != screenQueue || !strings.Contains(out, "Ana ") || !strings.Contains(out, "★") {
		t.Errorf("queue after the results:\n%s", out)
	}
}
