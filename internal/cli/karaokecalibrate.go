package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/ui"
)

func newKaraokeCalibrateCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "calibrate",
		Short: i18n.T("cmd.karaoke.calibrate.short"),
		Long:  i18n.T("cmd.karaoke.calibrate.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// --yes (the toolbox-wide flag) skips the question; without a
			// terminal there is nobody to ask, so it is required.
			if !e.assumeYes {
				if !e.interactive() {
					return fmt.Errorf("%s", i18n.T("karaoke.cal.need_yes"))
				}
				var ok bool
				err := huh.NewForm(huh.NewGroup(
					huh.NewConfirm().
						Title(i18n.T("karaoke.cal.confirm.title")).
						Description(i18n.T("karaoke.cal.warning")).
						Affirmative(i18n.T("karaoke.cal.confirm.yes")).
						Negative(i18n.T("karaoke.cal.confirm.no")).
						Value(&ok),
				)).RunWithContext(ctx)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("%s", i18n.T("karaoke.cal.declined"))
				}
			} else {
				ui.Println(ui.Warning(i18n.T("karaoke.cal.warning")))
			}
			return runKaraokeCalibrate(ctx, karaokeAppFor(e), ui.Out)
		},
	}
}

// runKaraokeCalibrate plays the sweep, prints what was measured for every
// input, and saves the result.
func runKaraokeCalibrate(ctx context.Context, app *karaokeApp, w io.Writer) error {
	announced := map[calibrate.Stage]bool{}
	res, err := app.Calibrate(ctx, func(stage calibrate.Stage, _ float64) {
		if announced[stage] {
			return
		}
		announced[stage] = true
		fmt.Fprintln(w, ui.Muted.Render(ui.GlyphBullet+" "+i18n.T("karaoke.cal.stage."+string(stage))+"..."))
	})
	if err != nil {
		return calibrateError(err)
	}

	fmt.Fprintln(w)
	for _, in := range res.Inputs {
		if in.NoEchoPath {
			fmt.Fprintln(w, ui.Warning(i18n.Tf("karaoke.doctor.cal_input_noecho", in.Channel)))
		} else {
			fmt.Fprintln(w, ui.Success(i18n.Tf("karaoke.cal.input", in.Channel,
				in.DelayMs, in.TailMs, in.ERLEdB, in.ResidualFloorDBFS)))
		}
		for _, warn := range in.Warnings {
			fmt.Fprintln(w, "    "+ui.Warning(warn))
		}
	}

	if err := app.SaveCalibration(res); err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Success(i18n.Tf("karaoke.cal.saved", res.Device, res.SampleRate)))
	return nil
}

// calibrateError turns a clipped sweep into advice a person can act on.
func calibrateError(err error) error {
	if errors.Is(err, calibrate.ErrClipping) {
		return fmt.Errorf("%s", i18n.T("karaoke.cal.clipping"))
	}
	return err
}
