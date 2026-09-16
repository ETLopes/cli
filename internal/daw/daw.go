// Package daw defines what the application needs from an audio workstation.
//
// The interface is written in studio terms -- cue mixes, instruments, effects
// -- so the domain layer never learns about track indexes, FX slots or OSC
// addresses. Those belong to whichever adapter implements this.
package daw

import (
	"context"
	"fmt"

	"github.com/ETLopes/cli/internal/studio"
)

// Info describes a connected workstation.
type Info struct {
	// Name identifies the workstation, e.g. "REAPER".
	Name string
	// Version is whatever the workstation reports.
	Version string
	// Project is the currently open project name, empty if unsaved.
	Project string
}

// Action is one change the adapter made, or would make, during setup.
type Action struct {
	// Kind is a short verb: "created", "repaired", "unchanged".
	Kind string
	// Object is what was acted on, e.g. `track "Guitar"`.
	Object string
	// Detail explains the change, e.g. "input 3, mono".
	Detail string
}

func (a Action) String() string {
	if a.Detail == "" {
		return fmt.Sprintf("%s %s", a.Kind, a.Object)
	}
	return fmt.Sprintf("%s %s (%s)", a.Kind, a.Object, a.Detail)
}

// SetupReport is the outcome of reconciling the workstation's topology with
// the studio's.
type SetupReport struct {
	Actions []Action
}

// Changed reports whether setup altered anything, which is what makes a
// repeated run observably a no-op.
func (r SetupReport) Changed() bool {
	for _, a := range r.Actions {
		if a.Kind != "unchanged" {
			return true
		}
	}
	return false
}

// Counts summarises actions by kind.
func (r SetupReport) Counts() map[string]int {
	out := map[string]int{}
	for _, a := range r.Actions {
		out[a.Kind]++
	}
	return out
}

// DAW is the control surface the application drives.
//
// Every method takes a context: these are network calls to another process
// that may be busy, mid-render, or showing a modal dialog, and hanging the
// TUI on one of them would be worse than failing.
type DAW interface {
	// Ping confirms the workstation is reachable and reports what it is.
	Ping(ctx context.Context) (Info, error)

	// Setup brings the workstation's topology in line with the studio's,
	// creating what is missing and repairing what is wrong. It must be safe
	// to call repeatedly and must not disturb tracks it does not manage.
	Setup(ctx context.Context) (SetupReport, error)

	// Snapshot reads the workstation's current state back into the domain
	// model, so desired and actual state can be compared.
	Snapshot(ctx context.Context) (*studio.Session, error)

	// SetSendLevel sets one instrument's send into one cue mix.
	SetSendLevel(ctx context.Context, cueID int, instrumentID string, level studio.Level) error

	// SetEffect enables or disables one effect in an instrument's chain.
	SetEffect(ctx context.Context, instrumentID, effectID string, enabled bool) error

	// SetChannel applies an input's whole strip: gain, tone, dynamics,
	// placement and level.
	SetChannel(ctx context.Context, instrumentID string, c studio.Channel) error

	// SetTuning configures a pitch-correction effect: which notes it may snap
	// to, and how fast it gets there.
	SetTuning(ctx context.Context, instrumentID, effectID string, t studio.Tuning) error

	// SetMonitorVolume sets the control-room level.
	SetMonitorVolume(ctx context.Context, level studio.Level) error

	// SetMonitorMute mutes or unmutes the control room.
	SetMonitorMute(ctx context.Context, muted bool) error

	// Save persists the workstation's project.
	Save(ctx context.Context) error
}

// Difference is one way the workstation disagrees with the session.
type Difference struct {
	// What names the thing that differs, e.g. "cue 1 guitar".
	What string
	// Desired is the session's value, rendered for display.
	Desired string
	// Actual is the workstation's value, rendered for display.
	Actual string
}

func (d Difference) String() string {
	return fmt.Sprintf("%s: %s in session, %s in DAW", d.What, d.Desired, d.Actual)
}

// Diff compares a desired session against one read back from the workstation.
//
// It lives here rather than in the adapter because it is the same comparison
// regardless of which workstation produced the snapshot.
func Diff(desired, actual *studio.Session) []Difference {
	var out []Difference

	for _, cue := range desired.Cues() {
		actualCue, err := actual.Cue(cue.ID)
		if err != nil {
			continue
		}
		for _, in := range studio.Instruments() {
			want, got := cue.Level(in.ID), actualCue.Level(in.ID)
			if want != got {
				out = append(out, Difference{
					What:    fmt.Sprintf("cue %d %s", cue.ID, in.ID),
					Desired: want.String() + " dB",
					Actual:  got.String() + " dB",
				})
			}
		}
	}

	for _, in := range studio.Instruments() {
		for _, eff := range studio.Chain(in.ID) {
			want := desired.EffectEnabled(in.ID, eff.ID)
			got := actual.EffectEnabled(in.ID, eff.ID)
			if want != got {
				out = append(out, Difference{
					What:    fmt.Sprintf("%s %s", in.ID, eff.ID),
					Desired: onOff(want),
					Actual:  onOff(got),
				})
			}
		}
	}

	dm, am := desired.Monitor(), actual.Monitor()
	if dm.Volume != am.Volume {
		out = append(out, Difference{
			What: "monitor volume", Desired: dm.Volume.String() + " dB", Actual: am.Volume.String() + " dB",
		})
	}
	if dm.Muted != am.Muted {
		out = append(out, Difference{
			What: "monitor mute", Desired: onOff(dm.Muted), Actual: onOff(am.Muted),
		})
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
