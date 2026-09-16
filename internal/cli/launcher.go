package cli

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/ui"
)

// tool is one entry in the toolbox launcher.
type tool struct {
	// Name is the subcommand that runs this tool non-interactively.
	Name string
	// Short describes the tool in one line.
	Short string
	// Glyph is a small visual marker, purely decorative.
	Glyph string
	// Run starts the tool's interactive flow.
	Run func(ctx context.Context) error
}

// tools lists everything the launcher offers, in display order.
//
// This has to stay in step with the subcommands the root registers; a tool
// missing here is invisible to anyone who runs the toolbox with no arguments,
// which is the only discovery route there is. A test cross-checks the two.
func tools(e *env) []tool {
	return []tool{
		{
			Name:  "dtx",
			Short: i18n.T("tool.dtx.short"),
			Glyph: "♪",
			Run:   func(ctx context.Context) error { return runPrep(ctx, e, nil) },
		},
		{
			Name:  "studio",
			Short: i18n.T("tool.studio.short"),
			Glyph: "♫",
			Run:   func(ctx context.Context) error { return runStudioTUI(ctx, e) },
		},
	}
}

// runLauncher shows the toolbox menu and runs whichever tool is chosen.
//
// The chosen tool runs after the menu has exited rather than from inside it:
// tools drive their own Bubble Tea programs, and two programs competing for
// the same terminal would fight over input and rendering.
func runLauncher(ctx context.Context, e *env) error {
	available := tools(e)
	if len(available) == 0 {
		return fmt.Errorf("no tools are available")
	}

	final, err := tea.NewProgram(newLauncherModel(available)).Run()
	if err != nil {
		return err
	}
	m, ok := final.(launcherModel)
	if !ok {
		return fmt.Errorf("unexpected launcher state")
	}
	if m.quit || m.chosen < 0 {
		return nil
	}
	return available[m.chosen].Run(ctx)
}

type launcherModel struct {
	tools   []tool
	cursor  int
	chosen  int
	quit    bool
	spinner spinner.Model
}

func newLauncherModel(ts []tool) launcherModel {
	sp := spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(ui.Accent))
	return launcherModel{tools: ts, chosen: -1, spinner: sp}
}

func (m launcherModel) Init() tea.Cmd { return m.spinner.Tick }

func (m launcherModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quit = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(m.tools)-1 {
				m.cursor++
			}
			return m, nil
		case "home", "g":
			m.cursor = 0
			return m, nil
		case "end", "G":
			m.cursor = len(m.tools) - 1
			return m, nil
		case "enter", " ":
			m.chosen = m.cursor
			return m, tea.Quit
		}
		// A digit jumps straight to that entry, which is faster than
		// arrowing once the list grows. The length is checked first: indexing
		// the key name before knowing it is non-empty would panic.
		if key := msg.String(); len(key) == 1 {
			if n := int(key[0]) - '1'; n >= 0 && n < len(m.tools) {
				m.chosen = n
				return m, tea.Quit
			}
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m launcherModel) View() tea.View {
	// Collapse on exit so the chosen tool starts against a clean screen.
	if m.quit || m.chosen >= 0 {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString("\n  " + ui.Title.Render("cli") + "  " + ui.Muted.Render(i18n.T("toolbox.tagline")))
	b.WriteString("\n\n")

	nameWidth := 0
	for _, t := range m.tools {
		if n := lipgloss.Width(t.Name); n > nameWidth {
			nameWidth = n
		}
	}

	for i, t := range m.tools {
		selected := i == m.cursor
		marker := "  "
		name := ui.Pad(t.Name, nameWidth)
		if selected {
			marker = ui.Accent.Render("▸ ")
			name = ui.Accent.Bold(true).Render(name)
		} else {
			name = ui.Heading.Render(name)
		}
		b.WriteString(fmt.Sprintf("  %s%s %s  %s\n",
			marker,
			ui.Muted.Render(fmt.Sprintf("%d", i+1)),
			name,
			ui.Muted.Render(t.Short)))
	}

	b.WriteString("\n  " + ui.Muted.Render(i18n.T("toolbox.launcher_keys")) + "\n")
	return tea.NewView(b.String())
}
