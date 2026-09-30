package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/song"
	"github.com/ETLopes/cli/internal/ui"
)

func newKaraokeExportCmd(e *env) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export <entry-id|url|song-dir>",
		Short: i18n.T("cmd.karaoke.export.short"),
		Long:  i18n.T("cmd.karaoke.export.long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKaraokeExport(cmd.Context(), karaokeAppFor(e), ui.Out, args[0], out)
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", i18n.T("karaoke.export.flag.out"))
	return cmd
}

// runKaraokeExport resolves what the user named to a prepared song, writes the
// UltraStar folder and reports where it went.
func runKaraokeExport(ctx context.Context, app *karaokeApp, w io.Writer, target, outDir string) error {
	dir, err := app.resolveSongDir(target)
	if err != nil {
		return err
	}
	res, err := app.ExportDir(ctx, dir, outDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, ui.Success(i18n.T("karaoke.export.done")))
	fmt.Fprintln(w, ui.KeyValue(i18n.T("karaoke.export.folder"), res.Dir, 8))
	fmt.Fprintln(w, ui.KeyValue(i18n.T("karaoke.export.chart"), res.Chart, 8))
	for _, warn := range res.Warnings {
		fmt.Fprintln(w, ui.Warning(string(warn)))
	}
	return nil
}

// resolveSongDir finds the prepared song a command-line argument names: a queue
// entry by ID, an entry by URL (any form of the same video), or a song
// directory.
func (a *karaokeApp) resolveSongDir(target string) (string, error) {
	store, _, err := a.OpenQueue()
	if err != nil {
		return "", err
	}
	videoID, hasVideo := song.VideoID(target)
	for _, en := range store.Entries() {
		if en.ID == target || en.URL == target || (hasVideo && en.VideoID == videoID) {
			if en.SongDir == "" {
				return "", fmt.Errorf("%s", i18n.Tf("karaoke.err.not_ready", entryName(en)))
			}
			return en.SongDir, nil
		}
	}
	if _, err := os.Stat(filepath.Join(target, song.ManifestName)); err == nil {
		return target, nil
	}
	return "", fmt.Errorf("%s", i18n.Tf("karaoke.export.unknown", target))
}

func newKaraokeReplayCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:    "replay <bundle-dir>",
		Short:  i18n.T("cmd.karaoke.replay.short"),
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKaraokeReplay(cmd.Context(), karaokeAppFor(e), ui.Out, args[0])
		},
	}
}

// runKaraokeReplay runs a recorded session through the live pipeline again with
// the stored calibration and song, and prints the scores and the per-mic
// metrics that canceller tuning needs.
func runKaraokeReplay(ctx context.Context, app *karaokeApp, w io.Writer, bundleDir string) error {
	b, err := live.ReadBundle(bundleDir)
	if err != nil {
		return err
	}
	sg, err := song.Load(b.SongDir)
	if err != nil {
		return err
	}
	key := calibrate.Key{Device: b.Calibration.Device, SampleRate: b.Calibration.SampleRate, Outputs: b.Calibration.Outputs}
	cal, ok, err := calibrate.Load(app.calibrationDir, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", i18n.Tf("karaoke.replay.no_calibration", key.Device, key.SampleRate))
	}
	res, metrics, err := live.Replay(ctx, bundleDir, cal, sg)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.replay.scores")))
	for _, p := range res.Players {
		fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.replay.player", p.Name, p.Channel, p.Score, p.InTunePercent)))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.replay.metrics")))
	for _, m := range metrics.Mics {
		fmt.Fprintln(w, "  "+i18n.Tf("karaoke.replay.mic", m.Name, m.Channel, m.VoicedFraction*100, m.EchoReductionDB, m.MeanInputDBFS))
	}
	return nil
}
