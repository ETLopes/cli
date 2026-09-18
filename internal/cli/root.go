// Package cli defines the dtx command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// version is overridden at build time with -ldflags "-X ...cmd.version=v1.2.3".
var version = "dev"

// env carries the shared state every command needs.
type env struct {
	v       *viper.Viper
	cfg     config.Config
	studio  config.Studio
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

	// The language has to be settled before the command tree is built. Cobra
	// captures every Short and Long at construction, so a language chosen in
	// PersistentPreRunE arrives too late to affect a word of the help.
	lang, langErr := resolveLanguage()
	i18n.Use(lang)

	// The rig, for the same reason: the studio's per-instrument commands are
	// generated from it, so a rig with a second guitar needs to be known
	// before the tree exists or `cli studio guitar2` is not a command.
	topoErr := resolveTopology()

	root := newRootCmd(e)

	// Signals cancel the context so external tools are killed and partial
	// artifacts are left in a state a later run can resume from.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A level argument such as "-3" is indistinguishable from a flag bundle to
	// the flag parser, so the argument list is rewritten before it is handed
	// over. Without this, half the studio's commands are unusable.
	root.SetArgs(terminateFlagsBeforeNegativeNumber(os.Args[1:]))

	// Reported here rather than thrown away, but only after the command tree
	// exists, so --help still works when the configured language is nonsense.
	if langErr != nil {
		ui.Println(ui.Failure(langErr.Error()))
		return 1
	}
	if topoErr != nil {
		ui.Println(ui.Failure(topoErr.Error()))
		return 1
	}

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

// resolveLanguage reads the language before anything else exists to read it
// with: the environment, then the config file if one names a language.
//
// It reads the file with its own viper rather than the shared one. Binding an
// environment variable to a key makes viper return empty for that key when the
// variable is unset, which shadowed the config file entirely and let an
// invalid language through unreported.
func resolveLanguage() (i18n.Lang, error) {
	lang := i18n.Detect()

	probe := configProbe()
	// A file that cannot be read is reported properly by the ordinary load a
	// moment later; failing twice over one problem helps nobody.
	if err := probe.ReadInConfig(); err != nil {
		return lang, nil
	}

	configured := probe.GetString(config.KeyLang)
	if configured == "" {
		return lang, nil
	}
	parsed, ok := i18n.Parse(configured)
	if !ok {
		return lang, fmt.Errorf("invalid configuration: %s must be one of: %s (got %q)",
			config.KeyLang, langList(), configured)
	}
	return parsed, nil
}

// resolveTopology installs the rig before the command tree is built, for the
// same reason the language is settled first: cobra captures its subcommands at
// construction, and the studio generates one per instrument.
//
// A missing or unreadable file leaves the built-in rig in force and is
// reported by the ordinary load a moment later.
func resolveTopology() error {
	probe := configProbe()
	if err := probe.ReadInConfig(); err != nil {
		return nil
	}
	t, err := config.LoadTopology(probe)
	if err != nil {
		return err
	}
	return studio.Use(t)
}

// configProbe reads the file the run will use, for the settings that have to
// be known before the command tree exists. --config is taken straight from the
// arguments because the flag parser has not run yet.
func configProbe() *viper.Viper {
	probe := viper.New()
	if path := configFlagValue(os.Args[1:]); path != "" {
		probe.SetConfigFile(path)
		return probe
	}
	probe.SetConfigName(config.FileName)
	probe.AddConfigPath(config.Dir())
	return probe
}

// configFlagValue finds the --config argument, in either of the two spellings
// cobra accepts for it.
func configFlagValue(args []string) string {
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			return v
		}
	}
	return ""
}

// langList names the supported languages, for an error message.
func langList() string {
	names := make([]string, 0, len(i18n.Supported()))
	for _, l := range i18n.Supported() {
		names = append(names, string(l))
	}
	return strings.Join(names, ", ")
}

// negativeNumber matches an argument that is a negative number rather than a
// flag, e.g. "-3" or "-2.5".
var negativeNumber = regexp.MustCompile(`^-\d+(\.\d+)?$`)

// terminateFlagsBeforeNegativeNumber inserts "--" ahead of the first negative
// number in the argument list.
//
// pflag offers no way to declare that a command takes numeric arguments, and
// it reads "-2" as the shorthand flags -2. Terminating flag parsing just
// before such an argument lets it through while leaving any flags that came
// earlier to parse normally.
func terminateFlagsBeforeNegativeNumber(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args // the caller already terminated flag parsing
		}
		if negativeNumber.MatchString(a) {
			out := make([]string, 0, len(args)+1)
			out = append(out, args[:i]...)
			out = append(out, "--")
			return append(out, args[i:]...)
		}
	}
	return args
}

// rootDescription builds the help text from the tool list, so the tools named
// here cannot drift from the ones the launcher offers. Keeping a second copy
// by hand is how studio came to be missing from both.
func rootDescription(e *env) string {
	var b strings.Builder
	b.WriteString(ui.Title.Render("cli"))
	b.WriteString(` is a personal toolbox. Each tool lives under its own
subcommand, and every tool works interactively when run without arguments.

Tools:
`)
	width := 0
	for _, t := range tools(e) {
		if n := len(t.Name); n > width {
			width = n
		}
	}
	for _, t := range tools(e) {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, t.Name, t.Short)
	}
	return strings.TrimRight(b.String(), "\n")
}

func newRootCmd(e *env) *cobra.Command {
	var cfgFile string

	cmd := &cobra.Command{
		Use:           "cli",
		Short:         "A personal toolbox of day-to-day tools",
		Long:          rootDescription(e),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return e.setup(cmd, cfgFile)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// With no tool named, offer the launcher. Without a terminal to
			// drive it there is nothing to select with, so fall back to the
			// help text, which keeps `cli | cat` and scripts working.
			if !e.interactive() {
				return cmd.Help()
			}
			return runLauncher(cmd.Context(), e)
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

	cmd.AddCommand(newDTXCmd(e), newStudioCmd(e), newConfigCmd(e), newVersionCmd())
	return cmd
}

// setup loads configuration and installs the logger. It runs before every
// command.
func (e *env) setup(cmd *cobra.Command, cfgFile string) error {
	config.Bind(e.v)
	config.BindStudio(e.v)
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

	studioCfg, err := config.LoadStudio(e.v)
	if err != nil {
		return err
	}
	e.studio = studioCfg

	// The rig is installed before any command runs, so every one of them sees
	// the same inputs and outputs the user described.
	topology, err := config.LoadTopology(e.v)
	if err != nil {
		return err
	}
	if err := studio.Use(topology); err != nil {
		return err
	}

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
	"out":                  config.KeyOutputDir,
	"model":                config.KeyModel,
	"device":               config.KeyDevice,
	"shifts":               config.KeyShifts,
	"jobs":                 config.KeyJobs,
	"normalize":            config.KeyNormalize,
	"limit":                config.KeyLimit,
	"formats":              config.KeyFormats,
	"usb":                  config.KeyUSBPath,
	"cookies-from-browser": config.KeyCookies,
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cli version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ui.Println(ui.Title.Render("cli") + " " + version)
			return nil
		},
	}
}
