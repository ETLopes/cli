package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// The patch page is where the rig is described: what is plugged into each
// input, and which input it is on. It is separate from the console because it
// changes the shape of the studio rather than the sound of it: a wrong value
// here is a channel that records silence, which is worth a deliberate save
// rather than taking effect as you scroll.
//
// Changing what an input holds changes its pedalboard too, since a chain
// follows the kind of instrument. Three inputs set to guitar give three
// guitarists three separate boards.

// maxInput is the highest channel the interface offers.
const maxInput = 20

// patchEntry is one input's assignment while it is being edited.
type patchEntry struct {
	ID      string
	Name    string
	Kind    studio.Kind
	Channel int
	Stereo  bool

	// orig is what this input held when the page opened. Cycling through the
	// kinds to look at them should not cost an instrument its identity, which
	// is what its saved levels and effects are keyed by, so coming back to
	// where it started restores exactly that.
	origID   string
	origName string
	origKind studio.Kind
}

// patchEntries reads the current assignment into an editable form.
func patchEntries() []patchEntry {
	var out []patchEntry
	for _, in := range studio.Instruments() {
		k := in.EffectiveKind()
		out = append(out, patchEntry{
			ID: in.ID, Name: in.Name, Kind: k,
			Channel: in.Input, Stereo: in.Mode == studio.Stereo,
			origID: in.ID, origName: in.Name, origKind: k,
		})
	}
	return out
}

// kindLabel is how a kind reads on screen, in the interface language.
func kindLabel(k studio.Kind) string { return i18n.T("kind." + string(k)) }

// otherInstruments renders every entry but one, which is what naming a new
// instrument needs: the IDs already taken, and how many of each kind there
// are. Pass a negative index to include them all.
func otherInstruments(entries []patchEntry, skip int) []studio.Instrument {
	var out []studio.Instrument
	for i, e := range entries {
		if i == skip {
			continue
		}
		out = append(out, studio.Instrument{
			ID: e.ID, Name: e.Name, Kind: e.Kind, Input: e.Channel,
		})
	}
	return out
}

// setPatchKind changes what is plugged into an input, renaming it to suit.
func setPatchKind(entries []patchEntry, idx int, k studio.Kind) {
	e := &entries[idx]
	if k == e.Kind {
		return
	}
	e.Kind = k
	if k == e.origKind && e.origID != "" {
		e.ID, e.Name = e.origID, e.origName
		return
	}
	e.ID, e.Name = studio.NextInstrument(k, otherInstruments(entries, idx))
}

// addPatchEntry plugs something into the first free input. The kind is the
// unopinionated one, because the interface cannot know what was just carried
// through the door.
func addPatchEntry(entries []patchEntry) ([]patchEntry, error) {
	used := map[int]bool{}
	for _, e := range entries {
		for _, c := range e.channels() {
			used[c] = true
		}
	}
	channel := 0
	for c := 1; c <= maxInput; c++ {
		if !used[c] {
			channel = c
			break
		}
	}
	if channel == 0 {
		return entries, errors.New(i18n.Tf("patch.full", maxInput))
	}
	id, name := studio.NextInstrument(studio.KindLine, otherInstruments(entries, -1))
	return append(entries, patchEntry{
		ID: id, Name: name, Kind: studio.KindLine, Channel: channel,
	}), nil
}

// removePatchEntry unplugs an input. A studio with nothing plugged into it is
// not a studio, so the last one stays.
func removePatchEntry(entries []patchEntry, idx int) ([]patchEntry, error) {
	if len(entries) <= 1 {
		return entries, errors.New(i18n.T("patch.last"))
	}
	// The capped slice forces a copy rather than writing over the entries the
	// caller still holds.
	return append(entries[:idx:idx], entries[idx+1:]...), nil
}

// channels lists the hardware channels an entry occupies.
func (p patchEntry) channels() []int {
	if p.Stereo {
		return []int{p.Channel, p.Channel + 1}
	}
	return []int{p.Channel}
}

// patchConflict names the instrument an entry collides with, if any.
//
// Reported per row rather than only on save: finding out that two inputs
// clash after committing means undoing a change you have already forgotten
// the shape of.
func patchConflict(entries []patchEntry, idx int) string {
	mine := entries[idx].channels()
	for i, other := range entries {
		if i == idx {
			continue
		}
		for _, a := range mine {
			for _, b := range other.channels() {
				if a == b {
					return other.Name
				}
			}
		}
	}
	return ""
}

// patchTopology turns the edited entries into a topology, keeping the buses
// as they are: this page moves inputs, not outputs.
func patchTopology(entries []patchEntry) studio.Topology {
	current := studio.Current()
	t := studio.Topology{Main: current.Main, Cues: current.Cues}
	for _, e := range entries {
		mode := studio.Mono
		if e.Stereo {
			mode = studio.Stereo
		}
		t.Instruments = append(t.Instruments, studio.Instrument{
			ID: e.ID, Name: e.Name, Input: e.Channel, Mode: mode, Kind: e.Kind,
		})
	}
	return t
}

// savePatch validates the assignment, writes it to the config file and puts it
// into force. Writing before applying means a rejected topology never becomes
// the file's problem.
func savePatch(entries []patchEntry) error {
	t := patchTopology(entries)
	if err := t.Validate(); err != nil {
		return err
	}

	path := filepath.Join(config.Dir(), config.FileName+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	v := viper.New()
	v.SetConfigFile(path)
	// Read first, so everything else in the file survives.
	_ = v.ReadInConfig()

	inputs := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		entry := map[string]any{
			"id": e.ID, "name": e.Name, "channel": e.Channel, "kind": string(e.Kind),
		}
		if e.Stereo {
			entry["mode"] = "stereo"
		}
		inputs = append(inputs, entry)
	}
	v.Set(config.KeyInputs, inputs)

	// The outputs are written too. A file that names inputs and not outputs
	// would be read as a partial rig, and merging it with defaults would
	// route audio somewhere nobody described.
	cur := studio.Current()
	cues := make([][]int, 0, len(cur.Cues))
	for _, c := range cur.Cues {
		cues = append(cues, c.Output.Channels())
	}
	v.Set(config.KeyMainOut, []int{cur.Main.Output.Left, cur.Main.Output.Right})
	v.Set(config.KeyCueOuts, cues)

	if err := v.WriteConfigAs(path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return studio.Use(t)
}

// renderPatch draws the input assignment.
func renderPatch(entries []patchEntry, cursor int, width int) string {
	var b strings.Builder

	nameWidth, kindWidth := 0, 0
	for _, e := range entries {
		if n := len(e.Name); n > nameWidth {
			nameWidth = n
		}
		if n := len(kindLabel(e.Kind)); n > kindWidth {
			kindWidth = n
		}
	}

	for i, e := range entries {
		marker := "   "
		name := ui.Pad(e.Name, nameWidth)
		if i == cursor {
			marker = ui.Accent.Render(" ▸ ")
			name = ui.Accent.Bold(true).Render(name)
		} else {
			name = ui.Muted.Render(name)
		}
		b.WriteString("  " + marker + name + "  ")

		// What is plugged in, which is also what pedalboard the input gets.
		// No angle brackets around it: those mark what the arrow keys move,
		// and the arrows belong to the input channel.
		kind := ui.Pad(kindLabel(e.Kind), kindWidth)
		if i == cursor {
			b.WriteString(ui.Accent.Bold(true).Render(kind) + "  ")
		} else {
			b.WriteString(ui.Heading.Render(kind) + "  ")
		}

		channel := fmt.Sprintf("%s %d", i18n.T("patch.input"), e.Channel)
		if e.Stereo {
			channel = fmt.Sprintf("%s %d/%d", i18n.T("patch.inputs"), e.Channel, e.Channel+1)
		}
		if i == cursor {
			b.WriteString(ui.Accent.Render("‹ " + ui.Pad(channel, 12) + " ›"))
		} else {
			b.WriteString(ui.Heading.Render(ui.Pad(channel, 16)))
		}

		mode := i18n.T("patch.mono")
		if e.Stereo {
			mode = i18n.T("patch.stereo")
		}
		b.WriteString("  " + ui.Muted.Render(ui.Pad(mode, 8)))

		if other := patchConflict(entries, i); other != "" {
			b.WriteString("  " + ui.Err.Render(i18n.Tf("patch.conflict", other)))
		}
		b.WriteString("\n")
	}

	// Which channels are free, so moving something does not need counting.
	used := map[int]bool{}
	for _, e := range entries {
		for _, c := range e.channels() {
			used[c] = true
		}
	}
	var free []string
	for c := 1; c <= maxInput; c++ {
		if !used[c] {
			free = append(free, fmt.Sprint(c))
		}
	}
	if len(free) > 0 {
		b.WriteString("\n  " + ui.Muted.Render(i18n.T("patch.free")+": "+strings.Join(free, " ")) + "\n")
	}
	return b.String()
}
