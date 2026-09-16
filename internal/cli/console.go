package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// The console lays the studio out the way a mixing desk does: one strip per
// input, side by side, every control in the same place on every strip. That
// arrangement is the point -- it is read by position rather than by label, so
// the eye finds the guitar's mid control without looking for the word "mid".

// consoleRow is one row of the desk: a control that appears on every strip.
type consoleRow struct {
	control studio.StripControl
	// cue is the cue number for an aux row, 0 for anything else.
	cue int
}

// label is what prints down the left edge.
func (r consoleRow) label() string {
	if r.cue > 0 {
		return fmt.Sprintf("AUX%d", r.cue)
	}
	return r.control.Label()
}

// isSwitch reports whether the row holds switches rather than values.
func (r consoleRow) isSwitch() bool { return r.cue == 0 && r.control.IsSwitch() }

// consoleRows builds the desk's rows, with the aux sends sitting after the
// dynamics as they do on a real strip. How many there are follows the rig.
func consoleRows() []consoleRow {
	var rows []consoleRow
	for _, c := range studio.StripControls() {
		if c == studio.ControlPan {
			for _, bus := range studio.CueBuses() {
				rows = append(rows, consoleRow{cue: bus.CueID})
			}
		}
		rows = append(rows, consoleRow{control: c})
	}
	return rows
}

// consoleValue renders one cell.
func consoleValue(s *studio.Session, instrumentID string, r consoleRow) string {
	if r.cue > 0 {
		cue, err := s.Cue(r.cue)
		if err != nil {
			return "-"
		}
		return cue.Level(instrumentID).String()
	}
	ch, _ := s.Channel(instrumentID)
	switch r.control {
	case studio.ControlTrim:
		return ch.Trim.String()
	case studio.ControlHigh:
		return ch.EQ.High.String()
	case studio.ControlMid:
		return ch.EQ.Mid.String()
	case studio.ControlMidFreq:
		return formatHz(ch.EQ.MidFreq)
	case studio.ControlLow:
		return ch.EQ.Low.String()
	case studio.ControlComp:
		return fmt.Sprintf("%.0f%%", ch.Comp*100)
	case studio.ControlPan:
		return ch.PanLabel()
	case studio.ControlMute:
		return switchMark(ch.Muted)
	case studio.ControlSolo:
		return switchMark(ch.Soloed)
	case studio.ControlFader:
		return ch.Fader.String()
	}
	return ""
}

func switchMark(on bool) string {
	if on {
		return "ON"
	}
	return "·"
}

// formatHz keeps frequencies to a width a column can hold.
func formatHz(hz float64) string {
	if hz >= 1000 {
		return fmt.Sprintf("%.1fk", hz/1000)
	}
	return fmt.Sprintf("%.0f", hz)
}

// adjustConsole moves one control by a step, where step is the number of
// notches: one press is one notch, shift is four.
func adjustConsole(s *studio.Session, instrumentID string, r consoleRow, notches int) (string, error) {
	if r.cue > 0 {
		adj := studio.Adjustment{Delta: studio.Level(notches), Relative: true}
		change, err := s.SetCueLevel(r.cue, instrumentID, adj)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("aux%d %s", r.cue, change), nil
	}

	ch, _ := s.Channel(instrumentID)
	step := studio.Level(notches)
	switch r.control {
	case studio.ControlTrim:
		ch.Trim = clampLevel(ch.Trim+step, studio.MinTrim, studio.MaxTrim)
	case studio.ControlHigh:
		ch.EQ.High = clampLevel(ch.EQ.High+step, studio.MinEQGain, studio.MaxEQGain)
	case studio.ControlMid:
		ch.EQ.Mid = clampLevel(ch.EQ.Mid+step, studio.MinEQGain, studio.MaxEQGain)
	case studio.ControlLow:
		ch.EQ.Low = clampLevel(ch.EQ.Low+step, studio.MinEQGain, studio.MaxEQGain)
	case studio.ControlMidFreq:
		// The sweep moves in ratios, not hertz: a fixed step would crawl at
		// the bottom of the range and leap at the top.
		factor := 1.0
		for i := 0; i < abs(notches); i++ {
			if notches > 0 {
				factor *= 1.15
			} else {
				factor /= 1.15
			}
		}
		next := ch.EQ.MidFreq * factor
		if next < studio.MinMidFreq {
			next = studio.MinMidFreq
		}
		if next > studio.MaxMidFreq {
			next = studio.MaxMidFreq
		}
		ch.EQ.MidFreq = next
	case studio.ControlComp:
		ch.Comp = clampFloat(ch.Comp+float64(notches)*0.05, 0, 1)
	case studio.ControlPan:
		ch.Pan = clampFloat(ch.Pan+float64(notches)*0.1, studio.PanLeft, studio.PanRight)
	case studio.ControlFader:
		ch.Fader = clampLevel(ch.Fader+step, studio.MinFader, studio.MaxFader)
	case studio.ControlMute, studio.ControlSolo:
		return "", nil
	}
	if err := s.SetChannel(instrumentID, ch); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s", strings.ToLower(r.control.Label()),
		consoleValue(s, instrumentID, r)), nil
}

// toggleConsole flips a switch row.
func toggleConsole(s *studio.Session, instrumentID string, r consoleRow) (string, error) {
	ch, _ := s.Channel(instrumentID)
	switch r.control {
	case studio.ControlMute:
		ch.Muted = !ch.Muted
	case studio.ControlSolo:
		ch.Soloed = !ch.Soloed
	default:
		return "", nil
	}
	if err := s.SetChannel(instrumentID, ch); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s", strings.ToLower(r.control.Label()),
		consoleValue(s, instrumentID, r)), nil
}

// syncConsole pushes one strip, or one aux send, to the workstation.
func syncConsole(ctx context.Context, d daw.DAW, s *studio.Session, instrumentID string, r consoleRow) error {
	if r.cue > 0 {
		cue, err := s.Cue(r.cue)
		if err != nil {
			return err
		}
		return d.SetSendLevel(ctx, r.cue, instrumentID, cue.Level(instrumentID))
	}
	ch, _ := s.Channel(instrumentID)
	return d.SetChannel(ctx, instrumentID, ch)
}

func clampLevel(v, lo, hi studio.Level) studio.Level {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// renderConsole draws the desk.
func renderConsole(s *studio.Session, rows []consoleRow, chanIdx, rowIdx int, width int) string {
	instruments := studio.Instruments()
	if len(instruments) == 0 {
		return "  no inputs configured\n"
	}

	// Columns are sized to the widest thing in them, so the grid stays in
	// line however long an instrument is named.
	const labelWidth = 6
	colWidth := 6
	for _, in := range instruments {
		if n := len(in.Name); n+1 > colWidth {
			colWidth = n + 1
		}
	}
	// Keep the desk inside the terminal rather than wrapping into nonsense.
	visible := len(instruments)
	if width > 0 {
		if fits := (width - labelWidth - 4) / colWidth; fits > 0 && fits < visible {
			visible = fits
		}
	}
	first := 0
	if chanIdx >= first+visible {
		first = chanIdx - visible + 1
	}
	last := min(first+visible, len(instruments))

	var b strings.Builder

	b.WriteString("  " + strings.Repeat(" ", labelWidth))
	for i := first; i < last; i++ {
		name := strings.ToUpper(instruments[i].Name)
		cell := ui.Pad(name, colWidth)
		if i == chanIdx {
			b.WriteString(ui.Accent.Bold(true).Render(cell))
		} else {
			b.WriteString(ui.Heading.Render(cell))
		}
	}
	b.WriteString("\n")

	for ri, r := range rows {
		// The fader is the one control a desk sets apart, so it gets a rule.
		if r.control == studio.ControlFader {
			b.WriteString("  " + ui.Muted.Render(strings.Repeat("─",
				labelWidth+(last-first)*colWidth)) + "\n")
		}

		label := ui.Pad(r.label(), labelWidth)
		if ri == rowIdx {
			b.WriteString("  " + ui.Accent.Render(label))
		} else {
			b.WriteString("  " + ui.Muted.Render(label))
		}

		for i := first; i < last; i++ {
			val := consoleValue(s, instruments[i].ID, r)
			cell := ui.Pad(val, colWidth)
			switch {
			case ri == rowIdx && i == chanIdx:
				b.WriteString(ui.Accent.Bold(true).Render(cell))
			case r.isSwitch() && val == "ON":
				// A mute or solo left on is worth seeing from across a room.
				if r.control == studio.ControlMute {
					b.WriteString(ui.Err.Render(cell))
				} else {
					b.WriteString(ui.Warn.Render(cell))
				}
			case i == chanIdx:
				b.WriteString(ui.Heading.Render(cell))
			default:
				b.WriteString(ui.Muted.Render(cell))
			}
		}
		b.WriteString("\n")
	}

	if last < len(instruments) || first > 0 {
		b.WriteString("  " + ui.Muted.Render(fmt.Sprintf(
			"showing %d-%d of %d inputs", first+1, last, len(instruments))) + "\n")
	}
	return b.String()
}

// parseConsoleLabel recovers the channel and row a push referred to, so a
// coalesced follow-up knows what to resend.
func parseConsoleLabel(label string, rows []consoleRow) (string, consoleRow, bool) {
	slash := strings.LastIndexByte(label, '/')
	if slash < 0 {
		return "", consoleRow{}, false
	}
	instrumentID, rowLabel := label[:slash], label[slash+1:]
	if _, ok := studio.LookupInstrument(instrumentID); !ok {
		return "", consoleRow{}, false
	}
	for _, r := range rows {
		if r.label() == rowLabel {
			return instrumentID, r, true
		}
	}
	return "", consoleRow{}, false
}
