package cli

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/ui"
)

// runConfigTUI opens the settings editor.
func runConfigTUI(e *env) error {
	m := newConfigModel(e)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return err
	}
	fm, ok := final.(configModel)
	if !ok || !fm.wrote {
		return nil
	}
	ui.Println(ui.Success(i18n.T("set.saved") + ": " + fm.path))
	return nil
}

type configModel struct {
	settings []setting
	headings []string
	// values holds the working copy, so nothing reaches the file until the
	// user asks for it.
	values map[string]string
	cursor int

	// editing holds the buffer while a text value is being typed.
	editing bool
	buffer  string

	dirty     bool
	wrote     bool
	path      string
	status    string
	statusErr bool
	quitting  bool
	width     int
	height    int
}

func newConfigModel(e *env) configModel {
	settings, headings := flatSettings()
	values := make(map[string]string, len(settings))
	for _, s := range settings {
		values[s.Key] = currentSetting(e, s.Key)
	}
	return configModel{
		settings: settings,
		headings: headings,
		values:   values,
		path:     filepath.Join(config.Dir(), config.FileName+".yaml"),
		width:    90,
		height:   30,
	}
}

// currentSetting reads a value as it stands, going through viper so defaults,
// the file and the environment all resolve the way they do everywhere else.
func currentSetting(e *env, key string) string {
	switch key {
	case config.KeyFormats:
		return strings.Join(e.v.GetStringSlice(key), ",")
	case config.KeyLang:
		// Read from the language actually in force: this key is shadowed by
		// its own environment binding, so asking viper returns empty.
		return string(i18n.Current())
	}
	return e.v.GetString(key)
}

func (m configModel) Init() tea.Cmd { return nil }

func (m configModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if m.editing {
			return m.handleEditKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m configModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.settings[m.cursor]

	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.settings)-1 {
			m.cursor++
		}
		return m, nil
	case "left", "right":
		step := 1
		if msg.String() == "left" {
			step = -1
		}
		switch s.Kind {
		case kindChoice:
			m.values[s.Key] = nextChoice(s, m.values[s.Key], step)
		case kindToggle:
			m.values[s.Key] = map[bool]string{true: "false", false: "true"}[m.values[s.Key] == "true"]
		default:
			return m, nil
		}
		m.dirty = true
		m.status, m.statusErr = "", false
		// The language is the one setting worth applying as it is chosen:
		// seeing the screen change is the proof it took.
		if s.Key == config.KeyLang {
			if l, ok := i18n.Parse(m.values[s.Key]); ok {
				i18n.Use(l)
				m.settings, m.headings = flatSettings()
			}
		}
		return m, nil
	case "enter":
		if s.Kind == kindChoice || s.Kind == kindToggle {
			return m.handleKey(tea.KeyPressMsg{Code: 'r', Text: "right"})
		}
		m.editing = true
		m.buffer = m.values[s.Key]
		return m, nil
	case "s":
		return m.save(), nil
	}
	return m, nil
}

func (m configModel) handleEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.settings[m.cursor]

	switch msg.String() {
	case "esc":
		m.editing, m.buffer = false, ""
		return m, nil
	case "enter":
		if err := s.validate(m.buffer); err != nil {
			m.status, m.statusErr = firstLine(err.Error()), true
			return m, nil
		}
		m.values[s.Key] = m.buffer
		m.editing, m.buffer = false, ""
		m.dirty = true
		m.status, m.statusErr = "", false
		return m, nil
	case "backspace":
		if r := []rune(m.buffer); len(r) > 0 {
			m.buffer = string(r[:len(r)-1])
		}
		return m, nil
	case "ctrl+u":
		m.buffer = ""
		return m, nil
	}

	// Only characters that belong in this value are taken, which is what
	// keeps a letter out of a port number.
	if text := msg.Text; text != "" {
		for _, r := range text {
			if s.acceptsRune(r) {
				m.buffer += string(r)
			}
		}
	}
	return m, nil
}

// save writes every changed setting, validating first so a bad value cannot
// reach the file.
func (m configModel) save() configModel {
	for _, s := range m.settings {
		if err := s.validate(m.values[s.Key]); err != nil {
			m.status, m.statusErr = firstLine(err.Error()), true
			return m
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		m.status, m.statusErr = err.Error(), true
		return m
	}

	v := viper.New()
	v.SetConfigFile(m.path)
	// Read what is there first, so anything not shown in this editor survives.
	_ = v.ReadInConfig()
	for _, s := range m.settings {
		value := m.values[s.Key]
		if s.Kind == kindList {
			var parts []string
			for _, p := range strings.Split(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					parts = append(parts, p)
				}
			}
			v.Set(s.Key, parts)
			continue
		}
		v.Set(s.Key, typedValue(value))
	}
	if err := v.WriteConfigAs(m.path); err != nil {
		m.status, m.statusErr = err.Error(), true
		return m
	}

	m.dirty, m.wrote = false, true
	m.status, m.statusErr = i18n.T("set.saved"), false
	return m
}

func (m configModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString("  " + ui.Title.Render("cli") + "  " + ui.Heading.Render(i18n.T("set.title")))
	if m.dirty {
		b.WriteString("   " + ui.Warn.Render("● "+i18n.T("set.unsaved")))
	}
	b.WriteString("\n\n")

	labelWidth := 0
	for _, s := range m.settings {
		if n := len(s.Label); n > labelWidth {
			labelWidth = n
		}
	}

	for i, s := range m.settings {
		if m.headings[i] != "" {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + ui.Heading.Render(m.headings[i]) + "\n")
		}

		marker := "   "
		label := ui.Pad(s.Label, labelWidth)
		if i == m.cursor {
			marker = ui.Accent.Render(" ▸ ")
			label = ui.Accent.Bold(true).Render(label)
		} else {
			label = ui.Muted.Render(label)
		}
		b.WriteString("  " + marker + label + "  ")

		switch {
		case m.editing && i == m.cursor:
			b.WriteString(ui.Accent.Render(m.buffer + "▏"))
		case s.Kind == kindChoice, s.Kind == kindToggle:
			v := displayValue(s, m.values[s.Key])
			if i == m.cursor {
				b.WriteString(ui.Accent.Render("‹ " + v + " ›"))
			} else {
				b.WriteString(ui.Heading.Render(v))
			}
		default:
			b.WriteString(ui.Heading.Render(truncate(displayValue(s, m.values[s.Key]),
				max(20, m.width-labelWidth-12))))
		}
		b.WriteString("\n")
	}

	// What the selected setting does, always in view.
	if help := m.settings[m.cursor].Help; help != "" {
		b.WriteString("\n")
		for _, line := range wrap(help, max(40, m.width-6)) {
			b.WriteString("  " + ui.Muted.Render(line) + "\n")
		}
	}

	if m.status != "" {
		b.WriteString("\n")
		if m.statusErr {
			b.WriteString("  " + ui.Warning(truncate(m.status, max(20, m.width-4))) + "\n")
		} else {
			b.WriteString("  " + ui.Success(m.status) + "\n")
		}
	}

	keys := i18n.T("set.keys")
	if m.editing {
		keys = i18n.T("set.keys.editing")
	}
	b.WriteString("\n  " + ui.Muted.Render(keys) + "\n")
	b.WriteString("  " + ui.Muted.Render(m.path) + "\n")

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
