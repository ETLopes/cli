package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ETLopes/cli/internal/karaoke/queue"
)

func TestStartSessionThroughLiveOpenScoresTheSingerAndNotTheEcho(t *testing.T) {
	app, singing := fxApp(t)
	sg := fxSong(t, filepath.Join(t.TempDir(), "song"))
	entry := queue.Entry{ID: "e1", Title: "Synthetic", State: queue.Ready, SongDir: sg.Dir}
	ctx := context.Background()

	if _, err := app.StartSession(ctx, entry, nil); !errors.Is(err, errNotCalibrated) {
		t.Fatalf("StartSession without a calibration: %v, want errNotCalibrated", err)
	}
	if st, err := app.Calibration(); err != nil || st.Found {
		t.Fatalf("Calibration() = %+v, %v; want not found", st, err)
	}

	cal, err := app.Calibrate(ctx, nil)
	fxMust(t, err)
	fxMust(t, app.SaveCalibration(cal))
	st, err := app.Calibration()
	fxMust(t, err)
	if !st.Found || st.Stale || st.Key.Device != "Fake Interface" || len(st.Result.Inputs) != 2 {
		t.Fatalf("Calibration() = %+v, want a fresh one for both inputs", st)
	}

	singing.Store(true) // the room was silent while it was measured
	s, err := app.StartSession(ctx, entry, nil)
	fxMust(t, err)
	defer s.Close()
	res, err := s.Wait()
	fxMust(t, err)
	if len(res.Players) != 2 || res.Incomplete {
		t.Fatalf("results = %+v", res)
	}
	t.Logf("singer %d, echo only %d", res.Players[0].Score, res.Players[1].Score)
	if got := res.Players[0].Score; got < 8000 {
		t.Errorf("the singer on input 1 scored %d, want at least 8000", got)
	}
	if got := res.Players[1].Score; got > 400 {
		t.Errorf("input 2 (echo only) scored %d, want near zero", got)
	}
	if res.Players[0].Name != "Ana" || res.Players[1].Channel != 2 {
		t.Errorf("players = %+v", res.Players)
	}
}
