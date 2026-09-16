package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/daw"
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
		ui.Println(ui.Muted.Render("  Saved to the session; run 'cli studio sync' once REAPER is available."))
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
		Short: "Control a REAPER-based home studio",
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
		newStudioInstallCmd(e),
		newStudioSetupCmd(e),
		newStudioStatusCmd(e),
		newStudioCueCmd(e),
		newStudioMonitorCmd(e),
		newStudioSyncCmd(e),
		newStudioSessionCmd(e),
	)
	// Per-instrument effect commands, so `cli studio guitar overdrive on`
	// reads the way the instruction is spoken.
	for _, id := range studio.InstrumentsWithChains() {
		cmd.AddCommand(newStudioFXCmd(e, id))
	}
	return cmd
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
			ui.Println(ui.KeyValue("startup", report.StartupPath, 8))
			ui.Println()
			ui.Println(ui.Warning("Restart REAPER so it loads the bridge."))
			return nil
		},
	}
}

func newStudioSetupCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Create or repair the studio topology in REAPER",
		Long: `Brings REAPER in line with the studio: one track per input, a MAIN bus
on outputs 1/2, and four cue buses on 3/4, 5/6, 7/8 and 9/10, each fed by
pre-fader sends from every instrument.

Safe to run repeatedly. Existing managed objects are reused, incorrect
routing is repaired, and tracks the studio does not manage are left alone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			report, err := s.dawc.Setup(cmd.Context())
			if err != nil {
				return err
			}

			for _, a := range report.Actions {
				switch a.Kind {
				case "created":
					ui.Println(ui.Success(a.Object) + ui.Muted.Render("  "+a.Detail))
				case "repaired":
					ui.Println(ui.Warning(a.Object) + ui.Muted.Render("  "+a.Detail))
				default:
					ui.Println(ui.Muted.Render("  " + ui.GlyphPending + " " + a.Object))
				}
			}

			counts := report.Counts()
			ui.Println()
			if !report.Changed() {
				ui.Println(ui.Success("already set up; nothing to change"))
			} else {
				ui.Println(ui.Success(fmt.Sprintf("%d created, %d repaired, %d unchanged",
					counts["created"], counts["repaired"], counts["unchanged"])))
			}
			return s.store.Save(s.session)
		},
	}
}

func newStudioStatusCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the connection and any drift from the session",
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
				ui.Println(ui.Success("REAPER matches the session"))
				return nil
			}
			ui.Println(ui.Warning(fmt.Sprintf("%d difference(s) from the session:", len(diffs))))
			for _, d := range diffs {
				ui.Println(ui.Muted.Render("    " + d.String()))
			}
			ui.Println()
			ui.Println(ui.Muted.Render("  Run 'cli studio sync' to make REAPER match."))
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
		Short: "Set an instrument's level in a headphone mix",
		Long: `Sets how loud one instrument is in one musician's headphones.

A signed level changes by that amount; an unsigned one sets it absolutely:

  cli studio cue 1 guitar +3     three decibels louder
  cli studio cue 1 bass -2       two decibels quieter
  cli studio cue 2 guitar 0      set to unity
  cli studio cue 3 dtx off       silence it`,
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
		Short: "Control the control-room monitors",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "volume <level>",
		Short: "Set the monitor level (never above unity)",
		Args:  cobra.ExactArgs(1),
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
					ui.Println(ui.Success("monitors muted"))
				} else {
					ui.Println(ui.Success("monitors unmuted"))
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
			enabled, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			if err := s.session.SetEffect(instrumentID, args[0], enabled); err != nil {
				return err
			}
			eff, _ := studio.LookupEffect(instrumentID, args[0])
			slog.Info("effect", "instrument", instrumentID, "effect", eff.ID, "enabled", enabled)

			if err := s.push(cmd.Context(), func(ctx context.Context) error {
				return s.dawc.SetEffect(ctx, instrumentID, eff.ID, enabled)
			}); err != nil {
				return err
			}
			ui.Println(ui.Success(fmt.Sprintf("%s %s: %s", in.Name, eff.Name, onOffLabel(enabled))))
			return nil
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

func newStudioSyncCmd(e *env) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Make REAPER match the session",
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
