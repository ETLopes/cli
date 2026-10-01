package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
)

func TestExportResolvesAQueueEntryAndWritesAChart(t *testing.T) {
	app, _ := fxApp(t)
	f := runnertest.New()
	f.HandleFunc("ffmpeg", nil, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(spec runner.Spec) error {
			dst := spec.Args[len(spec.Args)-1]
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dst, []byte("audio"), 0o644)
		}}
	})
	app.run = f
	sg := fxSong(t, filepath.Join(t.TempDir(), "song"))

	store, _, err := app.OpenQueue()
	fxMust(t, err)
	en, err := store.Add(kTestURL)
	fxMust(t, err)
	fxMust(t, store.Finish(en.ID, sg.Dir, "Synthetic"))

	outDir := t.TempDir()
	for _, target := range []string{en.ID, kTestURL} {
		var out bytes.Buffer
		if err := runKaraokeExport(context.Background(), app, &out, target, outDir); err != nil {
			t.Fatalf("export %q: %v", target, err)
		}
		text := plain(out.String())
		if !strings.Contains(text, "exported") || !strings.Contains(text, outDir) {
			t.Errorf("export %q output:\n%s", target, text)
		}
	}
	charts, _ := filepath.Glob(filepath.Join(outDir, "*", "*.txt"))
	if len(charts) != 1 {
		t.Fatalf("charts = %v, want one .txt", charts)
	}
	body, _ := os.ReadFile(charts[0])
	if !strings.Contains(string(body), "#VERSION:1.1.0") {
		t.Errorf("chart lacks the format version:\n%.300s", body)
	}

	// A bare song directory works too, and an unknown name says so.
	var out bytes.Buffer
	fxMust(t, runKaraokeExport(context.Background(), app, &out, sg.Dir, outDir))
	if err := runKaraokeExport(context.Background(), app, &out, "nonsense", outDir); err == nil ||
		!strings.Contains(err.Error(), "not a queued song") {
		t.Errorf("unknown target err = %v", err)
	}
}

func TestReplayPrintsScoresAndPerMicMetricsForARecordedSession(t *testing.T) {
	app, singing := fxApp(t)
	app.cfg.RecordDir = t.TempDir()
	sg := fxSong(t, filepath.Join(t.TempDir(), "song"))
	entry := queue.Entry{ID: "e1", Title: "Synthetic", State: queue.Ready, SongDir: sg.Dir}
	ctx := context.Background()

	cal, err := app.Calibrate(ctx, nil)
	fxMust(t, err)
	fxMust(t, app.SaveCalibration(cal))
	singing.Store(true)
	s, err := app.StartSession(ctx, entry, nil)
	fxMust(t, err)
	_, err = s.Wait()
	fxMust(t, err)
	fxMust(t, s.Close())

	bundles, _ := filepath.Glob(filepath.Join(app.cfg.RecordDir, "*"))
	if len(bundles) != 1 {
		t.Fatalf("recordings = %v, want one", bundles)
	}
	var out bytes.Buffer
	fxMust(t, runKaraokeReplay(ctx, app, &out, bundles[0]))
	text := plain(out.String())
	for _, want := range []string{"Ana (input 1):", "Bruno (input 2): 0/100", "voiced", "echo reduction"} {
		if !strings.Contains(text, want) {
			t.Errorf("replay output lacks %q:\n%s", want, text)
		}
	}
}
