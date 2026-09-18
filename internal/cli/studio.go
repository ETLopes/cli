package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/reaper"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// studioEnv carries the studio tool's working state: the desired session, the
// file it came from, and the workstation to push changes to.
type studioEnv struct {
	env     *env
	store   *studio.Store
	session *studio.Session
	dawc    daw.DAW
}

// openStudio loads the configured session, creating it on first use.
func openStudio(e *env) (*studioEnv, error) {
	store := studio.NewStore(e.studio.SessionDir)
	name := e.studio.Session

	var session *studio.Session
	if store.Exists(name) {
		loaded, err := store.Load(name)
		if err != nil {
			return nil, err
		}
		session = loaded
	} else {
		session = studio.NewSession(name)
	}

	// Anything the session lost on load is reported once, here, rather than
	// left in a field nobody reads.
	for _, w := range session.Warnings {
		ui.Println(ui.Warning(w))
	}

	return &studioEnv{
		env:     e,
		store:   store,
		session: session,
		dawc:    reaper.New(e.studio.ReaperHost, e.studio.ReaperPort),
	}, nil
}

// push applies a change to the workstation.
//
// A change is recorded in the session whether or not REAPER is reachable: the
// session is the desired state, and losing an edit because the DAW happened to
// be closed would be worse than applying it later. An unreachable workstation
// is reported rather than hidden, with the command that resolves it.
func (s *studioEnv) push(ctx context.Context, apply func(context.Context) error) error {
	if err := s.store.Save(s.session); err != nil {
		return err
	}
	if err := apply(ctx); err != nil {
		slog.Debug("workstation push failed", "error", err)
		ui.Println(ui.Warning(firstLine(err.Error())))
		ui.Println(ui.Muted.Render("  " + i18n.T("studio.saved_session")))
		return nil
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func newStudioCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "studio",
		Short: i18n.T("cmd.studio.short"),
		Long: `Drives a REAPER home studio in studio terms rather than DAW terms:
instruments, cue mixes, effects and monitoring.

One track per physical input. Headphone mixes are pre-fader sends off those
tracks, so changing the control-room mix never alters what a musician hears.

Run with no arguments for the interactive mixer.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !e.interactive() {
				return cmd.Help()
			}
			return runStudioTUI(cmd.Context(), e)
		},
	}

	cmd.AddCommand(
		newStudioInitCmd(e),
		newStudioInstallCmd(e),
		newStudioSetupCmd(e),
		newStudioStatusCmd(e),
		newStudioCueCmd(e),
		newStudioMonitorCmd(e),
		newStudioTuneCmd(e),
		newStudioSyncCmd(e),
		newStudioSessionCmd(e),
	)
	// Per-instrument effect commands, so `cli studio guitar overdrive on`
	// reads the way the instruction is spoken.
	for _, id := range studio.InstrumentsWithChains() {
		cmd.AddCommand(newStudioFXCmd(e, id))
	}
	cmd.AddCommand(newStudioMicCmd(e))
	return cmd
}

// newStudioInitCmd walks a fresh machine through everything at once.
func newStudioInitCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: i18n.T("cmd.init.short"),
		Long: `Does everything a new machine needs, in one command.

REAPER's web interface is off by default and is the only way in from
outside, so it is switched on by editing REAPER's configuration directly.
That file is rewritten when REAPER exits, so REAPER must be closed.

Afterwards, start REAPER and run this again to build the topology.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			port := e.studio.ReaperPort

			st, err := openStudio(e)
			if err != nil {
				return err
			}

			// Already working: go straight to the topology and skip the rest.
			if _, pingErr := st.dawc.Ping(ctx); pingErr == nil {
				ui.Println(ui.Success("REAPER is reachable and the bridge is loaded"))
				return runStudioSetup(ctx, st)
			}

			ui.Println(ui.Banner("first-run setup"))
			ui.Println()

			if reaper.IsREAPERRunning() {
				ui.Println(ui.Warning("REAPER is running."))
				ui.Println(ui.Muted.Render(
					"  It rewrites its configuration on exit, so a change made now would be lost."))
				ui.Println(ui.Muted.Render("  Quit REAPER, run 'cli studio init' again, then start it."))
				return nil
			}

			web, err := reaper.EnableWebInterface(port)
			if err != nil {
				return err
			}
			if web.AlreadyEnabled {
				ui.Println(ui.Success(fmt.Sprintf("web interface already enabled on port %d", port)))
			} else {
				ui.Println(ui.Success(fmt.Sprintf("enabled the web interface on port %d", port)))
				if web.Existing > 0 {
					ui.Println(ui.Muted.Render(fmt.Sprintf(
						"  kept the %d control surface(s) already configured", web.Existing)))
				}
			}

			report, err := reaper.Install()
			if err != nil {
				return err
			}
			for _, a := range report.Actions {
				ui.Println(ui.Success(a))
			}

			ui.Println()
			ui.Println(ui.Heading.Render("  Next: start REAPER, then run 'cli studio init' again."))
			ui.Println(ui.Muted.Render("  That second run builds the tracks, buses and routing."))
			return nil
		},
	}
}

// runStudioSetup builds the topology and reports what changed.
func runStudioSetup(ctx context.Context, st *studioEnv) error {
	report, err := st.dawc.Setup(ctx)
	if err != nil {
		return err
	}
	for _, a := range report.Actions {
		switch a.Kind {
		case "created":
			ui.Println(ui.Success(a.Object) + ui.Muted.Render("  "+a.Detail))
		case "repaired":
			ui.Println(ui.Warning(a.Object) + ui.Muted.Render("  "+a.Detail))
		case "removed", "retired":
			// A track leaving the studio is worth saying out loud rather than
			// listing quietly beside the ones that did not change.
			ui.Println(ui.Warning(a.Object) + ui.Muted.Render("  "+a.Detail))
		default:
			ui.Println(ui.Muted.Render("  " + ui.GlyphPending + " " + a.Object))
		}
	}
	counts := report.Counts()
	ui.Println()
	if !report.Changed() {
		ui.Println(ui.Success(i18n.T("setup.unchanged")))
	} else {
		summary := fmt.Sprintf("%d created, %d repaired, %d unchanged",
			counts["created"], counts["repaired"], counts["unchanged"])
		// Only mentioned when it happened, since most runs remove nothing.
		if n := counts["removed"]; n > 0 {
			summary += fmt.Sprintf(", %d removed", n)
		}
		if n := counts["retired"]; n > 0 {
			summary += fmt.Sprintf(", %d retired", n)
		}
		ui.Println(ui.Success(summary))
	}
	return st.store.Save(st.session)
}

func newStudioInstallCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the REAPER bridge script",
		Long: `Writes the bridge script into REAPER's Scripts directory and makes
REAPER load it at startup.

The bridge is needed because REAPER's web interface can read and write state
but cannot create tracks or assign hardware inputs, while ReaScript can do
both but is unreachable from outside REAPER.

An existing __startup.lua is appended to, never replaced.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := reaper.Install()
			if err != nil {
				return err
			}
			ui.Println(ui.Success("bridge installed"))
			for _, a := range report.Actions {
				ui.Println(ui.Muted.Render("  " + a))
			}
			ui.Println()
			ui.Println(ui.KeyValue("script", report.ScriptPath, 8))
			ui.Println(ui.KeyValue("action", report.RegistryPath, 8))
			ui.Println()
			ui.Println(ui.Warning(i18n.T("setup.restart")))
			return nil
		},
	}
}

func newStudioSetupCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: i18n.T("cmd.setup.short"),
		Long: `Brings REAPER in line with the studio: one track per input, a MAIN bus
on outputs 1/2, and four cue buses on 3/4, 5/6, 7/8 and 9/10, each fed by
pre-fader sends from every instrument.

Safe to run repeatedly. Existing managed objects are reused, incorrect
routing is repaired, and tracks the studio does not manage are left alone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStudio(e)
			if err != nil {
				return err
			}
			return runStudioSetup(cmd.Context(), st)
		},
	}
}

func newStudioStatusCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: i18n.T("cmd.status.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			ui.Println(ui.Banner("studio"))
			ui.Println()
			ui.Println(ui.KeyValue("session", s.session.Name, 10))
			ui.Println(ui.KeyValue("file", s.store.Path(s.session.Name), 10))
			ui.Println(ui.KeyValue("reaper", fmt.Sprintf("%s:%d",
				e.studio.ReaperHost, e.studio.ReaperPort), 10))
			ui.Println()

			info, err := s.dawc.Ping(cmd.Context())
			if err != nil {
				ui.Println(ui.Failure(firstLine(err.Error())))
				// Rendered a line at a time: styling a multi-line block pads
				// every line to the widest, leaving trailing whitespace.
				for _, line := range remainingLines(err.Error()) {
					ui.Println(ui.Muted.Render(line))
				}
				return nil
			}
			ui.Println(ui.Success("connected to " + info.Name + " " + info.Version))

			actual, err := s.dawc.Snapshot(cmd.Context())
			if err != nil {
				ui.Println(ui.Warning("could not read REAPER's state: " + firstLine(err.Error())))
				return nil
			}
			diffs := daw.Diff(s.session, actual)
			ui.Println()
			if len(diffs) == 0 {
				ui.Println(ui.Success(i18n.T("studio.matches")))
				return nil
			}
			ui.Println(ui.Warning(fmt.Sprintf("%d difference(s) from the session:", len(diffs))))
			for _, d := range diffs {
				ui.Println(ui.Muted.Render("    " + d.String()))
			}
			ui.Println()
			ui.Println(ui.Muted.Render("  " + i18n.T("studio.sync_hint")))
			return nil
		},
	}
}

// remainingLines returns everything after the first line, for guidance that
// follows a one-line summary.
func remainingLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}

func newStudioCueCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "cue <number> <instrument> <level>",
		Short: i18n.T("cmd.cue.short"),
		Long: `Sets how loud one instrument is in one musician's headphones.

A signed level changes by that amount; an unsigned one sets it absolutely:

  cli studio cue 1 guitar +3     three decibels louder
  cli studio cue 1 bass -2       two decibels quieter
  cli studio cue 2 guitar 0      set to unity
  cli studio cue 3 keyboard @-6  set to exactly -6 dB
  cli studio cue 3 dtx off       silence it

Because "-6" already means "six quieter", an exact negative level is
written with a leading "@".`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cueID, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("%q is not a cue number (1-%d)", args[0], studio.CueCount())
			}
			adj, err := studio.ParseAdjustment(args[2])
			if err != nil {
				return err
			}

			s, err := openStudio(e)
			if err != nil {
				return err
			}
			in, ok := studio.LookupInstrument(args[1])
			if !ok {
				return fmt.Errorf("unknown instrument %q (available: %s)",
					args[1], strings.Join(studio.InstrumentIDs(), ", "))
			}

			change, err := s.session.SetCueLevel(cueID, in.ID, adj)
			if err != nil {
				return err
			}
			slog.Info("cue level", "cue", cueID, "instrument", in.ID,
				"from", change.From.String(), "to", change.To.String())

			if err := s.push(cmd.Context(), func(ctx context.Context) error {
				return s.dawc.SetSendLevel(ctx, cueID, in.ID, change.To)
			}); err != nil {
				return err
			}
			ui.Println(ui.Success(fmt.Sprintf("cue %d %s: %s", cueID, in.Name, change)))
			return nil
		},
	}
}

func newStudioMonitorCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: i18n.T("cmd.monitor.short"),
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "volume <level>",
		Short: "Set the monitor level (never above unity)",
		Long: `Sets the control-room level. It is capped at unity: boosting the
speakers above the mix is never what anyone means, and getting it wrong is
painful.

  cli studio monitor volume -3     three decibels quieter
  cli studio monitor volume @-20   set to exactly -20 dB
  cli studio monitor volume 0      unity

Because "-20" already means "twenty quieter", an exact level is written
with a leading "@".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			adj, err := studio.ParseAdjustment(args[0])
			if err != nil {
				return err
			}
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			change, err := s.session.SetMonitorVolume(adj)
			if err != nil {
				return err
			}
			slog.Info("monitor volume", "from", change.From.String(), "to", change.To.String())
			if err := s.push(cmd.Context(), func(ctx context.Context) error {
				return s.dawc.SetMonitorVolume(ctx, change.To)
			}); err != nil {
				return err
			}
			ui.Println(ui.Success("monitor: " + change.String()))
			return nil
		},
	})

	for _, tc := range []struct {
		use, short string
		muted      bool
	}{
		{"mute", "Silence the monitors", true},
		{"unmute", "Unsilence the monitors", false},
	} {
		muted := tc.muted
		cmd.AddCommand(&cobra.Command{
			Use:   tc.use,
			Short: tc.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := openStudio(e)
				if err != nil {
					return err
				}
				s.session.SetMonitorMute(muted)
				slog.Info("monitor mute", "muted", muted)
				if err := s.push(cmd.Context(), func(ctx context.Context) error {
					return s.dawc.SetMonitorMute(ctx, muted)
				}); err != nil {
					return err
				}
				if muted {
					ui.Println(ui.Success(i18n.T("monitor.muted")))
				} else {
					ui.Println(ui.Success(i18n.T("monitor.unmuted")))
				}
				return nil
			},
		})
	}
	return cmd
}

// newStudioFXCmd builds the per-instrument effect command, e.g.
// `cli studio guitar overdrive on`.
func newStudioFXCmd(e *env, instrumentID string) *cobra.Command {
	in, _ := studio.LookupInstrument(instrumentID)
	return &cobra.Command{
		Use:       instrumentID + " <effect> <on|off>",
		Short:     "Toggle an effect on " + in.Name,
		Long:      in.Name + " chain: " + studio.DescribeChain(instrumentID),
		Args:      cobra.ExactArgs(2),
		ValidArgs: studio.EffectIDs(instrumentID),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleEffect(cmd.Context(), e, instrumentID, args[0], args[1])
		},
	}
}

// toggleEffect switches one effect and pushes the change, applying the
// effect's stored settings when it is switched on.
func toggleEffect(ctx context.Context, e *env, instrumentID, effectID, state string) error {
	enabled, err := parseOnOff(state)
	if err != nil {
		return err
	}
	s, err := openStudio(e)
	if err != nil {
		return err
	}
	in, _ := studio.LookupInstrument(instrumentID)
	if err := s.session.SetEffect(instrumentID, effectID, enabled); err != nil {
		return err
	}
	eff, _ := studio.LookupEffect(instrumentID, effectID)
	slog.Info("effect", "instrument", instrumentID, "effect", eff.ID, "enabled", enabled)

	if err := s.push(ctx, func(ctx context.Context) error {
		if err := s.dawc.SetEffect(ctx, instrumentID, eff.ID, enabled); err != nil {
			return err
		}
		// A corrector switched on with nothing configured sits at defaults
		// that correct too gently to notice, so its settings go with it.
		if t, ok := s.session.Tuning(instrumentID, eff.ID); ok && enabled {
			return s.dawc.SetTuning(ctx, instrumentID, eff.ID, t)
		}
		return nil
	}); err != nil {
		return err
	}

	ui.Println(ui.Success(fmt.Sprintf("%s %s: %s", in.Name, eff.Name, onOffLabel(enabled))))
	if t, ok := s.session.Tuning(instrumentID, eff.ID); ok && enabled {
		ui.Println(ui.Muted.Render("  snaps to: " + strings.Join(t.Notes(), " ")))
	}
	return nil
}

// newStudioMicCmd accepts the microphone number as its own word, so
// `cli studio mic 1 t-pain on` works alongside `cli studio mic1 t-pain on`.
func newStudioMicCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "mic <1|2> <effect> <on|off>",
		Short: "Toggle an effect on a microphone",
		Long: `The microphone number as a separate word.

  cli studio mic 1 t-pain on     obvious, robotic pitch snapping
  cli studio mic 1 t-pain off
  cli studio mic 2 reverb on`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := "mic" + strings.TrimSpace(args[0])
			if _, ok := studio.LookupInstrument(id); !ok {
				// Which microphones exist depends on the rig, so the error
				// names the ones actually plugged in rather than the two this
				// was first written against.
				var have []string
				for _, in := range studio.InstrumentsOfKind(studio.KindVocal) {
					have = append(have, in.ID)
				}
				if len(have) == 0 {
					return fmt.Errorf("there is no microphone %q; this studio has no vocal input", args[0])
				}
				return fmt.Errorf("there is no microphone %q; the studio has %s",
					args[0], strings.Join(have, ", "))
			}
			return toggleEffect(cmd.Context(), e, id, args[1], args[2])
		},
	}
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1", "enable", "enabled":
		return true, nil
	case "off", "false", "no", "0", "disable", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("%q is not on or off", s)
}

func onOffLabel(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// newStudioTuneCmd configures a pitch-correction effect.
func newStudioTuneCmd(e *env) *cobra.Command {
	var key, scale, effect string
	var retune int
	var depth float64
	var hard bool

	cmd := &cobra.Command{
		Use:   "tune <instrument>",
		Short: "Configure pitch correction on a vocal",
		Long: `Sets how pitch correction behaves: which notes it may snap to, and how
long it takes to get there.

Retune speed is the whole effect. At 250 ms the voice glides between notes
and the correction is inaudible. At 0 ms it jumps, and that jump is what
makes the processing obvious.

The scale matters as much. Chromatic leaves every semitone legal, so a voice
only ever moves to the nearest one -- a small, unremarkable correction.
Constraining to a key forces bigger, deliberate leaps.

  cli studio tune mic1 --hard --key A --scale minor
  cli studio tune mic1 --effect autotune --retune 40
  cli studio tune mic1 --key C --scale pentatonicminor --retune 0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStudio(e)
			if err != nil {
				return err
			}
			in, ok := studio.LookupInstrument(args[0])
			if !ok {
				return fmt.Errorf("unknown instrument %q", args[0])
			}

			current, ok := st.session.Tuning(in.ID, effect)
			if !ok {
				return fmt.Errorf("%s has no tunable effect %q (try: %s)",
					in.ID, effect, strings.Join(st.session.TunableEffects(in.ID), ", "))
			}

			// A preset sets everything at once; explicit flags then refine it,
			// so --hard --retune 20 does what it looks like.
			if hard {
				current = studio.HardTune(current.Key, current.Scale)
			}
			if cmd.Flags().Changed("key") {
				current.Key = key
			}
			if cmd.Flags().Changed("scale") {
				current.Scale = scale
			}
			if cmd.Flags().Changed("retune") {
				current.RetuneMs = retune
			}
			if cmd.Flags().Changed("depth") {
				current.Depth = depth / 100
			}
			current.Enabled = true

			if err := st.session.SetTuning(in.ID, effect, current); err != nil {
				return err
			}
			slog.Info("tuning", "instrument", in.ID, "effect", effect,
				"key", current.Key, "scale", current.Scale, "retune_ms", current.RetuneMs)

			if err := st.push(cmd.Context(), func(ctx context.Context) error {
				return st.dawc.SetTuning(ctx, in.ID, effect, current)
			}); err != nil {
				return err
			}
			ui.Println(ui.Success(fmt.Sprintf("%s %s: %s", in.Name, effect, current.Describe())))
			ui.Println(ui.Muted.Render("  snaps to: " + strings.Join(current.Notes(), " ")))
			return nil
		},
	}

	// Hidden. Turning the effect on should be the whole interface; these are
	// here for the rare case of needing to move off the defaults.
	cmd.Hidden = true
	cmd.Flags().StringVar(&effect, "effect", "hardtune", "which corrector to configure (hardtune, autotune)")
	cmd.Flags().StringVar(&key, "key", "A", "root note ("+strings.Join(studio.Keys(), " ")+")")
	cmd.Flags().StringVar(&scale, "scale", "minor", "scale ("+strings.Join(studio.ScaleIDs(), ", ")+")")
	cmd.Flags().IntVar(&retune, "retune", 0, "retune speed in ms; 0 snaps instantly")
	cmd.Flags().Float64Var(&depth, "depth", 100, "wet mix percent; below 100 blends untouched voice back in")
	cmd.Flags().BoolVar(&hard, "hard", false, "the aggressive preset: instant retune, fully wet")
	return cmd
}

func newStudioSyncCmd(e *env) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: i18n.T("cmd.sync.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			actual, err := s.dawc.Snapshot(ctx)
			if err != nil {
				return err
			}
			diffs := daw.Diff(s.session, actual)
			if len(diffs) == 0 {
				ui.Println(ui.Success("REAPER already matches the session"))
				return nil
			}
			for _, d := range diffs {
				ui.Println(ui.Muted.Render("  " + d.String()))
			}
			if dryRun {
				ui.Println()
				ui.Println(ui.Muted.Render(fmt.Sprintf("  %d change(s) would be applied", len(diffs))))
				return nil
			}

			if err := applySession(ctx, s); err != nil {
				return err
			}
			ui.Println()
			ui.Println(ui.Success(fmt.Sprintf("applied %d change(s)", len(diffs))))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	return cmd
}

// applySession pushes every value in the session to the workstation.
func applySession(ctx context.Context, s *studioEnv) error {
	for _, cue := range s.session.Cues() {
		for _, in := range studio.Instruments() {
			if err := s.dawc.SetSendLevel(ctx, cue.ID, in.ID, cue.Level(in.ID)); err != nil {
				return err
			}
		}
	}
	for _, in := range studio.Instruments() {
		for _, eff := range studio.Chain(in.ID) {
			enabled := s.session.EffectEnabled(in.ID, eff.ID)
			if err := s.dawc.SetEffect(ctx, in.ID, eff.ID, enabled); err != nil {
				return err
			}
		}
	}
	m := s.session.Monitor()
	if err := s.dawc.SetMonitorVolume(ctx, m.Volume); err != nil {
		return err
	}
	return s.dawc.SetMonitorMute(ctx, m.Muted)
}

func newStudioSessionCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage studio sessions",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List stored sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := studio.NewStore(e.studio.SessionDir)
			names, err := store.List()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				ui.Println(ui.Muted.Render("  no sessions yet in " + e.studio.SessionDir))
				return nil
			}
			for _, n := range names {
				marker := "  "
				if n == e.studio.Session {
					marker = ui.Accent.Render("▸ ")
				}
				ui.Println(marker + n)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "new <name>",
		Short: "Create a session with safe defaults",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := studio.NewStore(e.studio.SessionDir)
			if store.Exists(args[0]) {
				return fmt.Errorf("a session named %q already exists", args[0])
			}
			session := studio.NewSession(args[0])
			if err := store.Save(session); err != nil {
				return err
			}
			ui.Println(ui.Success("created session " + args[0]))
			ui.Println(ui.Muted.Render("  " + store.Path(args[0])))
			ui.Println(ui.Muted.Render("  monitors start muted at " +
				studio.SafeStartupLevel.String() + " dB"))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "save",
		Short: "Write the session and save REAPER's project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			if err := s.store.Save(s.session); err != nil {
				return err
			}
			ui.Println(ui.Success("saved " + s.store.Path(s.session.Name)))
			if err := s.dawc.Save(cmd.Context()); err != nil {
				ui.Println(ui.Warning("REAPER project not saved: " + firstLine(err.Error())))
				return nil
			}
			ui.Println(ui.Success("saved the REAPER project"))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the session as YAML",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			data, err := s.session.Marshal()
			if err != nil {
				return err
			}
			ui.Printf("%s", data)
			return nil
		},
	})
	return cmd
}
