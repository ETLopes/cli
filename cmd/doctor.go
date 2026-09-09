package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/eduardolopes/dtx/internal/deps"
	"github.com/eduardolopes/dtx/internal/runner"
	"github.com/eduardolopes/dtx/internal/separate"
	"github.com/eduardolopes/dtx/internal/ui"
)

func newDoctorCmd(e *env) *cobra.Command {
	var install, uninstall bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that the external tools dtx needs are installed",
		Long: `Reports the status of every external tool dtx drives.

ffmpeg, ffprobe and yt-dlp must be installed by you. Demucs can be managed
by dtx, which keeps it in its own Python environment so it never interferes
with any Python you use for your own work.`,
		Args: cobra.NoArgs,
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

			report := checker.Check(ctx)
			printReport(report)

			if install {
				if st, ok := report.Lookup(separate.Tool); ok && st.OK() {
					ui.Println()
					ui.Println(ui.Muted.Render("demucs is already installed; nothing to do"))
					return nil
				}
				ui.Println()
				if _, err := installDemucs(ctx, e, checker); err != nil {
					return err
				}
				return nil
			}

			if !report.Ready() {
				ui.Println()
				return fmt.Errorf("some required tools are missing (see above)")
			}
			ui.Println()
			ui.Println(ui.Success("everything dtx needs is installed"))
			return nil
		},
	}

	cmd.Flags().BoolVar(&install, "install", false, "install any missing tool dtx can manage")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove the managed demucs environment")
	cmd.MarkFlagsMutuallyExclusive("install", "uninstall")
	return cmd
}

// printReport renders the dependency table.
func printReport(r deps.Report) {
	ui.Println(ui.Banner("dependency check"))
	ui.Println()

	nameWidth := 4
	for _, t := range r.Tools {
		if len(t.Name) > nameWidth {
			nameWidth = len(t.Name)
		}
	}

	for _, t := range r.Tools {
		var status, detail string
		switch {
		case t.OK():
			status = ui.OK.Render(ui.GlyphOK)
			detail = t.Version
			if detail == "" {
				detail = "installed"
			}
		case t.Required:
			status = ui.Err.Render(ui.GlyphFail)
			detail = "missing"
		default:
			status = ui.Muted.Render(ui.GlyphPending)
			detail = "not installed (optional)"
		}
		ui.Println("  " + status + " " + ui.Pad(t.Name, nameWidth) + "  " + ui.Muted.Render(detail))

		if !t.OK() && t.Required {
			ui.Println("      " + ui.Muted.Render(t.Hint))
		}
	}

	if path := deps.ManagedDemucsPath(); path != "" {
		ui.Println()
		ui.Println(ui.Muted.Render("  managed environment: " + deps.VenvDir()))
	}
}
