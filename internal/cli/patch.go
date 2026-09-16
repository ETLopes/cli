package cli

import (
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

// The patch page is where an instrument is moved to a different input. It is
// separate from the console because it changes the shape of the studio rather
// than the sound of it: a wrong value here is a channel that records silence,
// which is worth a deliberate save rather than taking effect as you scroll.

// maxInput is the highest channel the interface offers.
const maxInput = 20

// patchEntry is one instrument's input assignment while it is being edited.
type patchEntry struct {
	ID      string
	Name    string
	Channel int
	Stereo  bool
}

// patchEntries reads the current assignment into an editable form.
func patchEntries() []patchEntry {
	var out []patchEntry
	for _, in := range studio.Instruments() {
		out = append(out, patchEntry{
			ID: in.ID, Name: in.Name, Channel: in.Input, Stereo: in.Mode == studio.Stereo,
		})
	}
	return out
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
			ID: e.ID, Name: e.Name, Input: e.Channel, Mode: mode,
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
		entry := map[string]any{"id": e.ID, "name": e.Name, "channel": e.Channel}
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
		cues = append(cues, []int{c.Output.Left, c.Output.Right})
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

	nameWidth := 0
	for _, e := range entries {
		if n := len(e.Name); n > nameWidth {
			nameWidth = n
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
