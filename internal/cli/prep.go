package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/dtxspec"
	"github.com/ETLopes/cli/internal/pipeline"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/ui"
	"github.com/ETLopes/cli/internal/youtube"
)

// registerPrepFlags defines the flags that control a pipeline run. They are
// attached to both `prep` and the root command so `dtx <url>` accepts them too.
func registerPrepFlags(fs *pflag.FlagSet) {
	d := config.Defaults()
	fs.StringP("out", "o", d.OutputDir, "directory to write results into")
	fs.StringP("model", "m", d.Model, "Demucs model ("+strings.Join(config.KnownModels, ", ")+")")
	fs.String("device", d.Device, "compute backend (auto, cpu, mps, cuda)")
	fs.Int("shifts", d.Shifts, "shift-trick passes; higher is better and proportionally slower")
	fs.Int("jobs", d.Jobs, "parallel Demucs workers (0 lets Demucs decide)")
	fs.Bool("normalize", d.Normalize, "loudness-normalize rendered mixes to -14 LUFS")
	fs.Bool("limit", d.Limit, "apply a brickwall limiter to avoid clipping")
	fs.StringSlice("formats", d.Formats, "extra formats to produce for sharing ("+strings.Join(audio.EncodingIDs(), ", ")+"); wav is always included")
	fs.String("usb", d.USBPath, "copy finished files to this drive's root directory")
	fs.String("cookies-from-browser", d.CookiesFromBrowser, "borrow cookies from a browser for restricted videos")
}

func newPrepCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prep [url]",
		Short: "Download a video and render DTX-PRO-ready practice tracks",
		Long: `Downloads a video's audio, splits it into instrument stems, and renders
every result as 44.1 kHz / 16-bit / stereo WAV that a DTX-PRO will play.

Produces the full mix, one "minus-one" mix per instrument, and each
isolated stem. Omit the URL to be prompted for one.`,
		Args:    cobra.MaximumNArgs(1),
		Aliases: []string{"do", "run"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPrep(cmd.Context(), e, args)
		},
	}
	registerPrepFlags(cmd.Flags())
	return cmd
}

// runPrep is the whole user-facing flow: work out what to fetch, make sure the
// tools exist, run the pipeline, and report what landed on disk.
func runPrep(ctx context.Context, e *env, args []string) error {
	req := pipeline.Request{
		OutputDir:          e.cfg.OutputDir,
		Model:              e.cfg.Model,
		Device:             e.cfg.Device,
		Shifts:             e.cfg.Shifts,
		Jobs:               e.cfg.Jobs,
		Normalize:          e.cfg.Normalize,
		Limit:              e.cfg.Limit,
		Formats:            e.cfg.Formats,
		USBPath:            e.cfg.USBPath,
		CookiesFromBrowser: e.cfg.CookiesFromBrowser,
	}
	if len(args) == 1 {
		req.URL = strings.TrimSpace(args[0])
	}

	// With no URL and no terminal to ask on, there is nothing to do.
	if req.URL == "" {
		if !e.interactive() {
			return fmt.Errorf("no URL given; pass one as an argument or run interactively")
		}
		if err := wizard(ctx, &req); err != nil {
			return err
		}
	}
	if err := validateURL(req.URL); err != nil {
		return err
	}

	tools, err := ensureTools(ctx, e)
	if err != nil {
		return err
	}

	r := runner.New()
	sep := separate.New(r)
	sep.Path = tools.demucsPath

	p := &pipeline.Pipeline{
		YouTube:   youtube.New(r),
		Audio:     audio.New(r),
		Separator: sep,
	}

	// The pipeline gets a context the UI can cancel independently of the
	// signal-derived one, so ctrl+c inside the live view is handled the same
	// way as a signal.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result *pipeline.Result
	if e.plain {
		reporter := ui.NewPlainReporter(ui.Out)
		result, err = p.Run(runCtx, req, reporter.Observe)
	} else {
		result, err = ui.RunPipeline(runCtx, cancel, "preparing practice tracks", func(observe pipeline.Observer) (*pipeline.Result, error) {
			return p.Run(runCtx, req, observe)
		})
	}
	if err != nil {
		return err
	}
	printSummary(result)
	return nil
}

// wizard collects the run's settings interactively.
func wizard(ctx context.Context, req *pipeline.Request) error {
	modelOptions := []huh.Option[string]{
		huh.NewOption("Standard - 4 stems, fastest", separate.ModelDefault),
		huh.NewOption("Fine-tuned - 4 stems, cleanest, ~4x slower", separate.ModelFineTuned),
		huh.NewOption("Six stems - adds guitar and piano, experimental", separate.ModelSixStem),
	}

	// WAV is listed and pre-selected because it is what the module plays, but
	// it is produced regardless of what is ticked here.
	selected := make(map[string]bool, len(req.Formats))
	for _, f := range req.Formats {
		selected[f] = true
	}
	selected[audio.EncodingWAV] = true

	var formatOptions []huh.Option[string]
	for _, enc := range audio.Encodings() {
		label := enc.Label
		if enc.Note != "" {
			label += " — " + enc.Note
		}
		formatOptions = append(formatOptions,
			huh.NewOption(label, enc.ID).Selected(selected[enc.ID]))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Video URL").
				Placeholder("https://www.youtube.com/watch?v=...").
				Value(&req.URL).
				Validate(func(s string) error { return validateURL(strings.TrimSpace(s)) }),
			huh.NewSelect[string]().
				Title("Separation quality").
				Description("Every model produces a minus-one mix per stem.").
				Options(modelOptions...).
				Value(&req.Model),
			huh.NewMultiSelect[string]().
				Title("Formats").
				Description("Each format gets its own folder. WAV is always produced.").
				Options(formatOptions...).
				Value(&req.Formats),
		),
	)
	if err := form.RunWithContext(ctx); err != nil {
		return err
	}
	req.URL = strings.TrimSpace(req.URL)
	return nil
}

// validateURL rejects input that clearly is not a fetchable address, so the
// user finds out immediately rather than after yt-dlp starts up.
func validateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("a video URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("that does not look like a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL is missing a host")
	}
	return nil
}

// printSummary reports what was produced, leading with the file the user is
// most likely to want: the mix with the drums taken out.
//
// Only the module WAVs are listed individually. Every format contains the same
// nine tracks, so listing all of them would run to dozens of near-identical
// lines; the per-format rollup carries what actually differs, which is size.
func printSummary(r *pipeline.Result) {
	ui.Println()
	ui.Println(ui.Success(ui.Heading.Render("Ready for the module")) +
		ui.Muted.Render(fmt.Sprintf("  %d files · %s · %s",
			len(r.Outputs), ui.HumanBytes(r.TotalBytes()), formatDuration(r.Elapsed))))
	ui.Println()
	ui.Println(ui.KeyValue("Track", r.Info.Title, 8))
	ui.Println(ui.KeyValue("Folder", r.Dir, 8))
	ui.Println(ui.KeyValue("Module", dtxspec.FormatDescription(), 8))
	ui.Println()

	wavs := outputsFor(r, audio.EncodingWAV)
	nameWidth := 4
	for _, o := range wavs {
		if n := len(o.Name()); n > nameWidth {
			nameWidth = n
		}
	}

	for _, group := range []struct {
		heading string
		kind    pipeline.Kind
	}{
		{"Play along (instrument removed)", pipeline.KindMinusOne},
		{"Full mix", pipeline.KindFull},
		{"Isolated stems", pipeline.KindStem},
	} {
		var rows []pipeline.Output
		for _, o := range wavs {
			if o.Kind == group.kind {
				rows = append(rows, o)
			}
		}
		if len(rows) == 0 {
			continue
		}
		ui.Println("  " + ui.Heading.Render(group.heading))
		for _, o := range rows {
			line := "    " + ui.Pad(o.Name(), nameWidth) + "  " +
				ui.Muted.Render(ui.Pad(ui.HumanBytes(o.Bytes), 9))
			if group.kind == pipeline.KindMinusOne && o.Instrument != "" {
				line += ui.Muted.Render("no " + o.Instrument)
			}
			ui.Println(line)
		}
		ui.Println()
	}

	printFormats(r)

	for _, w := range r.Warnings {
		ui.Println(ui.Warning(w))
	}
	if r.Exported != "" {
		ui.Println(ui.Success("copied the WAVs to " + r.Exported))
	}
}

// printFormats renders the per-format rollup.
func printFormats(r *pipeline.Result) {
	groups := r.ByEncoding()
	if len(groups) <= 1 {
		ui.Println(ui.Muted.Render("  Copy these to the root of a USB stick (not a folder) for the module to see them."))
		return
	}

	dirWidth, labelWidth := 4, 4
	for _, g := range groups {
		if n := len(g.Dir + "/"); n > dirWidth {
			dirWidth = n
		}
		if n := len(g.Encoding.Label); n > labelWidth {
			labelWidth = n
		}
	}

	ui.Println("  " + ui.Heading.Render("Formats"))
	for _, g := range groups {
		line := "    " + ui.Accent.Render(ui.Pad(g.Dir+"/", dirWidth)) + "  " +
			ui.Pad(g.Encoding.Label, labelWidth) + "  " +
			ui.Muted.Render(fmt.Sprintf("%2d files  %9s", g.Count, ui.HumanBytes(g.Bytes)))
		if g.Encoding.ID == audio.EncodingWAV {
			line += "  " + ui.Muted.Render("← copy to USB root")
		}
		ui.Println(line)
	}
	ui.Println()
	ui.Println(ui.Muted.Render("  Only the dtx/ WAVs play on the module; the rest are for sharing."))
}

// outputsFor returns the outputs written in one encoding.
func outputsFor(r *pipeline.Result, encoding string) []pipeline.Output {
	var out []pipeline.Output
	for _, o := range r.Outputs {
		if o.Encoding == encoding {
			out = append(out, o)
		}
	}
	return out
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
