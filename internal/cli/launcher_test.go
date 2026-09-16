package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ETLopes/cli/internal/studio"
)

func testTools(n int) []tool {
	out := make([]tool, n)
	for i := range out {
		out[i] = tool{Name: "tool", Short: "does a thing", Run: func(context.Context) error { return nil }}
	}
	return out
}

func press(m launcherModel, key string) launcherModel {
	next, _ := m.Update(tea.KeyPressMsg{Code: keyCode(key), Text: key})
	return next.(launcherModel)
}

// keyCode builds the rune code Bubble Tea reports for a printable key.
func keyCode(key string) rune {
	if len(key) == 1 {
		return rune(key[0])
	}
	return 0
}

func TestLauncherNavigationStaysInBounds(t *testing.T) {
	m := newLauncherModel(testTools(3))

	// Moving up from the top must not go negative.
	m = press(m, "k")
	if m.cursor != 0 {
		t.Errorf("cursor = %d after up at top, want 0", m.cursor)
	}

	m = press(m, "j")
	m = press(m, "j")
	m = press(m, "j")
	if m.cursor != 2 {
		t.Errorf("cursor = %d after moving past the end, want 2", m.cursor)
	}
}

func TestLauncherSelection(t *testing.T) {
	m := press(newLauncherModel(testTools(3)), "j")
	m = press(m, "enter")
	if m.chosen != 1 {
		t.Errorf("chosen = %d, want 1", m.chosen)
	}
}

func TestLauncherDigitShortcut(t *testing.T) {
	m := press(newLauncherModel(testTools(3)), "2")
	if m.chosen != 1 {
		t.Errorf("pressing 2 chose %d, want index 1", m.chosen)
	}

	// A digit beyond the list must be ignored, not select something.
	m = press(newLauncherModel(testTools(2)), "9")
	if m.chosen != -1 {
		t.Errorf("pressing 9 with 2 tools chose %d, want no selection", m.chosen)
	}
}

// Multi-character key names must not be indexed as if they were digits.
func TestLauncherIgnoresNonDigitKeys(t *testing.T) {
	for _, key := range []string{"ctrl+a", "shift+tab", "f1", "", "home"} {
		m := newLauncherModel(testTools(2))
		next, _ := m.Update(tea.KeyPressMsg{Text: key, Code: keyCode(key)})
		got := next.(launcherModel)
		if got.chosen != -1 && key != "home" {
			t.Errorf("key %q selected %d, want no selection", key, got.chosen)
		}
	}
}

func TestLauncherQuit(t *testing.T) {
	for _, key := range []string{"q", "esc", "ctrl+c"} {
		m := press(newLauncherModel(testTools(2)), key)
		if !m.quit {
			t.Errorf("key %q should quit", key)
		}
		if m.chosen != -1 {
			t.Errorf("key %q should not select anything", key)
		}
	}
}

func TestLauncherViewListsTools(t *testing.T) {
	m := newLauncherModel([]tool{
		{Name: "dtx", Short: "drum tracks"},
		{Name: "studio", Short: "home studio"},
	})
	view := m.View().Content
	for _, want := range []string{"dtx", "drum tracks", "studio", "home studio"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	// It collapses once a choice is made, so the tool starts on a clean screen.
	m.chosen = 0
	if got := m.View().Content; got != "" {
		t.Errorf("view after selection = %q, want empty", got)
	}
}

// The launcher is the only way a tool is discovered by someone who runs the
// toolbox with no arguments, so one missing from it is effectively invisible.
// This caught `studio` being absent after it was built and shipped.
func TestLauncherListsEveryTool(t *testing.T) {
	e := &env{}
	root := newRootCmd(e)

	// Subcommands that are tools rather than toolbox plumbing.
	plumbing := map[string]bool{
		"config": true, "version": true, "help": true, "completion": true,
	}
	var expected []string
	for _, c := range root.Commands() {
		name := c.Name()
		if plumbing[name] || c.Hidden {
			continue
		}
		expected = append(expected, name)
	}

	listed := map[string]bool{}
	for _, tl := range tools(e) {
		listed[tl.Name] = true
		if tl.Run == nil {
			t.Errorf("tool %q has no interactive entry point", tl.Name)
		}
		if tl.Short == "" {
			t.Errorf("tool %q has no description", tl.Name)
		}
	}

	for _, name := range expected {
		if !listed[name] {
			t.Errorf("subcommand %q is not offered by the launcher, so it is "+
				"invisible to anyone running the toolbox bare", name)
		}
	}
}

// The help text and the launcher must name the same tools; they were two
// hand-kept lists, and both went stale the same way.
func TestRootHelpNamesEveryTool(t *testing.T) {
	e := &env{}
	desc := rootDescription(e)
	for _, tl := range tools(e) {
		if !strings.Contains(desc, tl.Name) {
			t.Errorf("root help does not mention the tool %q", tl.Name)
		}
		if !strings.Contains(desc, tl.Short) {
			t.Errorf("root help does not describe the tool %q", tl.Name)
		}
	}
}

// --- console ---

// Every strip must offer the same controls in the same places: the desk is
// read by position, not by hunting for a label.
func TestConsoleRowsCoverTheWholeStrip(t *testing.T) {
	rows := consoleRows()
	seen := map[studio.StripControl]bool{}
	auxes := 0
	for _, r := range rows {
		if r.cue > 0 {
			auxes++
			continue
		}
		seen[r.control] = true
	}
	for _, c := range studio.StripControls() {
		if !seen[c] {
			t.Errorf("the console has no row for %q", c.Label())
		}
	}
	if auxes != studio.CueCount() {
		t.Errorf("got %d aux rows, want %d (one per cue mix)", auxes, studio.CueCount())
	}
	// The fader is the bottom of a strip, as on any desk.
	if last := rows[len(rows)-1]; last.control != studio.ControlFader {
		t.Errorf("the last row is %q, want the fader", last.label())
	}
}

func TestConsoleAdjustsAndClamps(t *testing.T) {
	s := studio.NewSession("t")
	rows := consoleRows()
	find := func(c studio.StripControl) consoleRow {
		for _, r := range rows {
			if r.cue == 0 && r.control == c {
				return r
			}
		}
		t.Fatalf("no row for %q", c)
		return consoleRow{}
	}

	if _, err := adjustConsole(s, "guitar", find(studio.ControlHigh), 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch, _ := s.Channel("guitar")
	if ch.EQ.High != 3 {
		t.Errorf("high = %v, want +3", ch.EQ.High)
	}

	// Winding past the end must stop there, not wrap or overshoot.
	for i := 0; i < 40; i++ {
		if _, err := adjustConsole(s, "guitar", find(studio.ControlHigh), 1); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	ch, _ = s.Channel("guitar")
	if ch.EQ.High != studio.MaxEQGain {
		t.Errorf("high = %v, want it clamped to %v", ch.EQ.High, studio.MaxEQGain)
	}

	// The mid sweep moves in ratios, so a step near the bottom is small in
	// hertz and a step near the top is large.
	before, _ := s.Channel("guitar")
	if _, err := adjustConsole(s, "guitar", find(studio.ControlMidFreq), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after, _ := s.Channel("guitar")
	if after.EQ.MidFreq <= before.EQ.MidFreq {
		t.Errorf("mid frequency did not rise: %v -> %v", before.EQ.MidFreq, after.EQ.MidFreq)
	}
	if after.EQ.MidFreq > studio.MaxMidFreq {
		t.Errorf("mid frequency %v exceeded the sweep", after.EQ.MidFreq)
	}
}

func TestConsoleAuxRowsDriveCueSends(t *testing.T) {
	s := studio.NewSession("t")
	var aux consoleRow
	for _, r := range consoleRows() {
		if r.cue == 1 {
			aux = r
		}
	}
	if _, err := adjustConsole(s, "guitar", aux, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cue, _ := s.Cue(1)
	if cue.Level("guitar") != 3 {
		t.Errorf("cue 1 guitar = %v, want the aux row to have moved it", cue.Level("guitar"))
	}
}

func TestConsoleSwitches(t *testing.T) {
	s := studio.NewSession("t")
	var mute, solo consoleRow
	for _, r := range consoleRows() {
		switch r.control {
		case studio.ControlMute:
			mute = r
		case studio.ControlSolo:
			solo = r
		}
	}
	if _, err := toggleConsole(s, "guitar", mute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Audible("guitar") {
		t.Error("a muted channel should not be audible")
	}

	// Soloing anything silences everything not soloed.
	if _, err := toggleConsole(s, "bass", solo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.Audible("bass") {
		t.Error("the soloed channel should be audible")
	}
	if s.Audible("keyboard") {
		t.Error("a channel that is not soloed should be silent while another is")
	}
}

// The desk must stay legible on a narrow terminal rather than wrapping.
func TestConsoleRenderFitsTheTerminal(t *testing.T) {
	s := studio.NewSession("t")
	rows := consoleRows()
	for _, width := range []int{40, 80, 200} {
		out := renderConsole(s, rows, 0, 0, width)
		for _, line := range strings.Split(out, "\n") {
			if lipgloss.Width(line) > width {
				t.Errorf("at width %d a line is %d wide:\n%s", width, lipgloss.Width(line), line)
				break
			}
		}
	}
}

func TestConsoleLabelRoundTrips(t *testing.T) {
	rows := consoleRows()
	for _, r := range rows {
		label := "guitar/" + r.label()
		inst, got, ok := parseConsoleLabel(label, rows)
		if !ok || inst != "guitar" || got.label() != r.label() {
			t.Errorf("label %q did not round-trip: %v %v %v", label, inst, got.label(), ok)
		}
	}
	if _, _, ok := parseConsoleLabel("nonsense", rows); ok {
		t.Error("a malformed label should not resolve")
	}
}
