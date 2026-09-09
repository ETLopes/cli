// Package cmd defines the dtx command-line interface.
package cmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/eduardolopes/dtx/internal/config"
	"github.com/eduardolopes/dtx/internal/ui"
)

// version is overridden at build time with -ldflags "-X ...cmd.version=v1.2.3".
var version = "dev"

// env carries the shared state every command needs.
type env struct {
	v       *viper.Viper
	cfg     config.Config
	verbose bool
	// plain disables the live terminal view, either because the user asked or
	// because stdout is not a terminal.
	plain bool
	// assumeYes skips confirmation prompts, for non-interactive use.
	assumeYes bool
}

// interactive reports whether it is safe to prompt the user.
func (e *env) interactive() bool {
	return !e.assumeYes && term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	e := &env{v: viper.New()}
	root := newRootCmd(e)

	// Signals cancel the context so external tools are killed and partial
	// artifacts are left in a state a later run can resume from.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		// A cancelled run is a deliberate user action, not a failure worth
		// dressing up as an error.
		if errors.Is(err, context.Canceled) {
			ui.Println(ui.Warning("cancelled"))
			return 130
		}
		ui.Println(ui.Failure(err.Error()))
		return 1
	}
	return 0
}

func newRootCmd(e *env) *cobra.Command {
	var cfgFile string

	cmd := &cobra.Command{
		Use:   "dtx [url]",
		Short: "Turn any video into DTX-PRO-ready drum practice tracks",
		Long: ui.Title.Render("dtx") + ` turns a video URL into a set of WAV files a Yamaha DTX-PRO
drum module will play: the original track, one mix per instrument removed
(so you can play the missing part yourself), and every isolated stem.

Audio is downloaded with yt-dlp, split into stems with Demucs, and rendered
with ffmpeg to the 44.1 kHz / 16-bit / stereo WAV the module requires.

Run with no arguments for an interactive session.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return e.setup(cmd, cfgFile)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bare `dtx` is the interactive entry point; `dtx <url>` is a
			// shorthand for `dtx prep <url>`.
			return runPrep(cmd.Context(), e, args)
		},
	}

	// Route Cobra's own output through the colour-aware writer so styled help
	// text is stripped rather than leaked as escape codes when piped.
	cmd.SetOut(ui.Out)
	cmd.SetErr(ui.Out)

	pf := cmd.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "", "config file (default "+config.Dir()+"/config.yaml)")
	pf.BoolVarP(&e.verbose, "verbose", "v", false, "log tool invocations and diagnostics to stderr")
	pf.BoolVar(&e.plain, "plain", false, "disable the live progress view")
	pf.BoolVarP(&e.assumeYes, "yes", "y", false, "assume yes for prompts; never prompt interactively")

	cmd.AddCommand(newPrepCmd(e), newDoctorCmd(e), newConfigCmd(e), newVersionCmd())
	// The prep flags are also accepted on the root command so `dtx <url>`
	// behaves identically to `dtx prep <url>`.
	registerPrepFlags(cmd.Flags())
	return cmd
}

// setup loads configuration and installs the logger. It runs before every
// command.
func (e *env) setup(cmd *cobra.Command, cfgFile string) error {
	config.Bind(e.v)
	if cfgFile != "" {
		e.v.SetConfigFile(cfgFile)
	}

	// Flags outrank the config file and environment, but only when the user
	// actually set them: an untouched flag must not clobber a config value
	// with its zero default.
	bindChangedFlags(cmd, e.v)

	cfg, err := config.Load(e.v)
	if err != nil {
		return err
	}
	e.cfg = cfg

	level := slog.LevelWarn
	if e.verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// A live redrawing view only makes sense on a real terminal.
	if !term.IsTerminal(os.Stdout.Fd()) {
		e.plain = true
	}
	slog.Debug("configuration loaded",
		"output_dir", cfg.OutputDir, "model", cfg.Model, "device", cfg.Device)
	return nil
}

// bindChangedFlags maps explicitly-set flags onto their config keys.
func bindChangedFlags(cmd *cobra.Command, v *viper.Viper) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		key, ok := flagToConfigKey[f.Name]
		if !ok {
			return
		}
		// A slice flag stringifies as "[a,b]", which would not survive being
		// unmarshalled back into a []string, so take the real slice instead.
		if sv, isSlice := f.Value.(pflag.SliceValue); isSlice {
			v.Set(key, sv.GetSlice())
			return
		}
		v.Set(key, f.Value.String())
	})
}

// flagToConfigKey maps flag names to config keys. Flags without an entry are
// command-scoped and never persisted.
var flagToConfigKey = map[string]string{
	"out":                  "output_dir",
	"model":                "model",
	"device":               "device",
	"shifts":               "shifts",
	"jobs":                 "jobs",
	"normalize":            "normalize",
	"limit":                "limit",
	"formats":              "formats",
	"usb":                  "usb_path",
	"cookies-from-browser": "cookies_from_browser",
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the dtx version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ui.Println(ui.Title.Render("dtx") + " " + version)
			return nil
		},
	}
}
