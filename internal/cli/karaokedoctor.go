package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/deps"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/ui"
)

// micTestDuration is how long doctor --mic listens.
const micTestDuration = time.Second

func newKaraokeDoctorCmd(e *env) *cobra.Command {
	var install, uninstall, mic bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: i18n.T("cmd.karaoke.doctor.short"),
		Long:  i18n.T("cmd.karaoke.doctor.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			checker := deps.NewChecker(runner.New())

			if uninstall {
				if err := deps.RemoveManagedDemucs(); err != nil {
					return fmt.Errorf("removing managed demucs: %w", err)
				}
				ui.Println(ui.Success("removed the managed demucs environment"))
				return nil
			}

			report := checker.CheckKaraoke(ctx)
			printReport(report)
			if install && !report.Ready() {
				ui.Println()
				if _, err := installDemucs(ctx, e, checker); err != nil {
					return err
				}
				report = checker.CheckKaraoke(ctx)
			}

			ui.Println()
			if err := runKaraokeDoctor(ctx, karaokeAppFor(e), ui.Out, mic); err != nil {
				return err
			}
			if !report.Ready() {
				ui.Println()
				return fmt.Errorf("%s", i18n.T("karaoke.doctor.missing_tools"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, i18n.T("karaoke.doctor.flag.install"))
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, i18n.T("karaoke.doctor.flag.uninstall"))
	cmd.Flags().BoolVar(&mic, "mic", false, i18n.T("karaoke.doctor.flag.mic"))
	cmd.MarkFlagsMutuallyExclusive("install", "uninstall")
	return cmd
}

// runKaraokeDoctor reports on the audio side: the devices, whether the
// configured ones exist, the calibration, and with mic, the live input levels.
func runKaraokeDoctor(ctx context.Context, app *karaokeApp, w io.Writer, mic bool) error {
	devs, err := app.backend.Devices()
	if errors.Is(err, audioio.ErrUnsupported) {
		fmt.Fprintln(w, ui.Warning(i18n.T("karaoke.doctor.unsupported")))
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.doctor.devices")))
	if len(devs) == 0 {
		fmt.Fprintln(w, "  "+ui.Warning(i18n.T("karaoke.err.no_devices")))
	}
	for _, d := range devs {
		mark := ""
		if d.IsDefault {
			mark = " " + i18n.T("karaoke.doctor.default")
		}
		fmt.Fprintf(w, "  %s  %s\n", d.Name+mark,
			ui.Muted.Render(i18n.Tf("karaoke.doctor.channels", d.CaptureChannels, d.PlaybackChannels, d.DefaultRate)))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.doctor.config")))
	dev, err := app.Device()
	if err != nil {
		fmt.Fprintln(w, "  "+ui.Failure(err.Error()))
		return nil
	}
	fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.doctor.device_ok", dev.Name)))
	for _, in := range app.cfg.Inputs {
		if in.Channel <= dev.CaptureChannels {
			fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.doctor.input_ok", in.Channel, in.Name)))
		} else {
			fmt.Fprintln(w, "  "+ui.Failure(i18n.Tf("karaoke.doctor.input_missing", in.Channel, in.Name, dev.CaptureChannels)))
		}
	}
	out := app.cfg.OutputPair()
	if out[0] <= dev.PlaybackChannels && out[1] <= dev.PlaybackChannels {
		fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.doctor.outputs_ok", out[0], out[1])))
	} else {
		fmt.Fprintln(w, "  "+ui.Failure(i18n.Tf("karaoke.doctor.outputs_missing", out[0], out[1], dev.PlaybackChannels)))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.doctor.calibration")))
	printCalibrationStatus(w, app)

	if mic {
		fmt.Fprintln(w)
		return printMicLevels(ctx, w, app)
	}
	return nil
}

// printCalibrationStatus says whether the setup is calibrated: missing, stale
// or ok, with its age and how well each input's echo is cancelled.
func printCalibrationStatus(w io.Writer, app *karaokeApp) {
	st, err := app.Calibration()
	if err != nil {
		fmt.Fprintln(w, "  "+ui.Failure(err.Error()))
		return
	}
	key := i18n.Tf("karaoke.doctor.cal_key", st.Key.Device, st.Key.SampleRate, st.Key.Outputs[0], st.Key.Outputs[1])
	switch {
	case !st.Found:
		fmt.Fprintln(w, "  "+ui.Warning(i18n.Tf("karaoke.doctor.cal_missing", key)))
		return
	case st.Stale:
		fmt.Fprintln(w, "  "+ui.Warning(i18n.Tf("karaoke.doctor.cal_stale", key, humanAge(st.Age), st.Reason)))
	default:
		fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.doctor.cal_ok", key, humanAge(st.Age))))
	}
	for _, in := range st.Result.Inputs {
		if in.NoEchoPath {
			fmt.Fprintln(w, "    "+ui.Warning(i18n.Tf("karaoke.doctor.cal_input_noecho", in.Channel)))
			continue
		}
		fmt.Fprintln(w, "    "+ui.Muted.Render(i18n.Tf("karaoke.doctor.cal_input", in.Channel, in.ERLEdB)))
	}
}

// printMicLevels records a second and reports each microphone's level.
func printMicLevels(ctx context.Context, w io.Writer, app *karaokeApp) error {
	fmt.Fprintln(w, ui.Heading.Render(i18n.T("karaoke.doctor.mic")))
	levels, err := app.MicLevels(ctx, micTestDuration)
	if err != nil {
		return err
	}
	allSilent := true
	for _, l := range levels {
		if l.Silent {
			fmt.Fprintln(w, "  "+ui.Warning(i18n.Tf("karaoke.doctor.mic_silent", l.Channel, l.Name)))
			continue
		}
		allSilent = false
		fmt.Fprintln(w, "  "+ui.Success(i18n.Tf("karaoke.doctor.mic_level", l.Channel, l.Name, dbfs(l.RMSDBFS), dbfs(l.PeakDBFS))))
	}
	if allSilent {
		fmt.Fprintln(w)
		fmt.Fprintln(w, ui.Warning(i18n.T("karaoke.doctor.mic_permission")))
	}
	return nil
}

func dbfs(v float64) float64 {
	if math.IsInf(v, -1) {
		return -120
	}
	return v
}

// humanAge renders how long ago something happened.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return i18n.T("karaoke.age.now")
	case d < time.Hour:
		return i18n.Tf("karaoke.age.minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return i18n.Tf("karaoke.age.hours", int(d.Hours()))
	}
	return i18n.Tf("karaoke.age.days", int(d.Hours()/24))
}
