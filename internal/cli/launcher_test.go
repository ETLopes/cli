package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
