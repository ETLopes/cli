package cli

import (
	"github.com/spf13/cobra"
)

// newDTXCmd builds the dtx tool: everything to do with turning a video into
// files a Yamaha DTX-PRO drum module will play.
//
// The tool is a command group rather than a single command so its own
// subcommands (prep, doctor) stay namespaced under it, leaving the toolbox
// root free for unrelated tools.
func newDTXCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dtx [url]",
		Short:   "Turn any video into DTX-PRO-ready drum practice tracks",
		Aliases: []string{"drums"},
		Long: `Turns a video URL into a set of WAV files a Yamaha DTX-PRO drum module
will play: the original track, one mix per instrument removed (so you can
play the missing part yourself), and every isolated stem.

Audio is downloaded with yt-dlp, split into stems with Demucs, and rendered
with ffmpeg to the 44.1 kHz / 16-bit / stereo WAV the module requires.

Run with no arguments for an interactive session.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// `cli dtx` is the interactive entry point, and `cli dtx <url>` a
			// shorthand for `cli dtx prep <url>`.
			return runPrep(cmd.Context(), e, args)
		},
	}

	cmd.AddCommand(newPrepCmd(e), newDoctorCmd(e))
	// The prep flags are accepted here too, so the shorthand behaves exactly
	// like the full `prep` invocation.
	registerPrepFlags(cmd.Flags())
	return cmd
}
