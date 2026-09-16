package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// runStudioTUI opens the interactive mixer.
func runStudioTUI(ctx context.Context, e *env) error {
	s, err := openStudio(e)
	if err != nil {
		return err
	}
	m := newStudioModel(ctx, s)

	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return err
	}
	fm, ok := final.(studioModel)
	if !ok {
		return nil
	}
	// The session is the desired state, so it is written on the way out
	// whether or not REAPER ever answered.
	if err := fm.studio.store.Save(fm.studio.session); err != nil {
		return err
	}
	ui.Println(ui.Success("saved " + fm.studio.store.Path(fm.studio.session.Name)))
	return nil
}

// tab is one page of the mixer.
type tab struct {
	title string
	rows  []row
}

// rowKind distinguishes a continuous control from a switch.
type rowKind int

const (
	rowLevel rowKind = iota
	rowToggle
)

// row is one editable control.
type row struct {
	label string
	kind  rowKind
	// value renders the current setting.
	value func() string
	// on reports switch state, for toggle rows.
	on func() bool
	// adjust changes a level row by delta decibels.
	adjust func(delta studio.Level) (studio.Change, error)
	// toggle flips a switch row.
	toggle func() error
	// sync pushes this row's current value to the workstation.
	sync func(context.Context, daw.DAW) error
}

type studioModel struct {
	ctx    context.Context
	studio *studioEnv

	tabs   []tab
	tabIdx int
	cursor int

	// connected tracks whether REAPER answered, so the header can say so
	// rather than leaving the user guessing why nothing is audible.
	connected bool
	dawName   string
	status    string
	statusErr bool
	quitting  bool
	width     int

	// Bridge calls are serialised, so a held arrow key would otherwise queue
	// one call per keypress and the mixer would keep moving long after the
	// key came up. Instead one push per row is in flight at a time and later
	// changes are collapsed into a single follow-up, which sends the value
	// the user actually settled on.
	pushing map[string]bool
	dirty   map[string]bool
	rows    map[string]row
}

func newStudioModel(ctx context.Context, s *studioEnv) studioModel {
	m := studioModel{
		ctx: ctx, studio: s, width: 90,
		pushing: map[string]bool{},
		dirty:   map[string]bool{},
		rows:    map[string]row{},
	}
	m.rebuild()
	return m
}

// rebuild constructs the tab contents from the session. Rows close over the
// session, so they always read live values rather than a stale copy.
func (m *studioModel) rebuild() {
	session := m.studio.session
	var tabs []tab

	for _, bus := range studio.CueBuses() {
		cueID := bus.CueID
		t := tab{title: bus.Name}
		for _, in := range studio.Instruments() {
			inst := in
			t.rows = append(t.rows, row{
				label: inst.Name,
				kind:  rowLevel,
				value: func() string {
					cue, err := session.Cue(cueID)
					if err != nil {
						return "-"
					}
					return cue.Level(inst.ID).String() + " dB"
				},
				adjust: func(delta studio.Level) (studio.Change, error) {
					return session.SetCueLevel(cueID, inst.ID,
						studio.Adjustment{Delta: delta, Relative: true})
				},
				sync: func(ctx context.Context, d daw.DAW) error {
					cue, err := session.Cue(cueID)
					if err != nil {
						return err
					}
					return d.SetSendLevel(ctx, cueID, inst.ID, cue.Level(inst.ID))
				},
			})
		}
		tabs = append(tabs, t)
	}

	fx := tab{title: "FX"}
	for _, id := range studio.InstrumentsWithChains() {
		inst, _ := studio.LookupInstrument(id)
		for _, e := range studio.Chain(id) {
			instID, eff := id, e
			fx.rows = append(fx.rows, row{
				label: inst.Name + "  " + eff.Name,
				kind:  rowToggle,
				on:    func() bool { return session.EffectEnabled(instID, eff.ID) },
				value: func() string { return onOffLabel(session.EffectEnabled(instID, eff.ID)) },
				toggle: func() error {
					return session.SetEffect(instID, eff.ID, !session.EffectEnabled(instID, eff.ID))
				},
				sync: func(ctx context.Context, d daw.DAW) error {
					return d.SetEffect(ctx, instID, eff.ID, session.EffectEnabled(instID, eff.ID))
				},
			})
		}
	}
	tabs = append(tabs, fx)

	mon := tab{title: "MONITOR"}
	mon.rows = append(mon.rows,
		row{
			label: "Yamaha HS5 level",
			kind:  rowLevel,
			value: func() string { return session.Monitor().Volume.String() + " dB" },
			adjust: func(delta studio.Level) (studio.Change, error) {
				return session.SetMonitorVolume(studio.Adjustment{Delta: delta, Relative: true})
			},
			sync: func(ctx context.Context, d daw.DAW) error {
				return d.SetMonitorVolume(ctx, session.Monitor().Volume)
			},
		},
		row{
			label:  "Muted",
			kind:   rowToggle,
			on:     func() bool { return session.Monitor().Muted },
			value:  func() string { return onOffLabel(session.Monitor().Muted) },
			toggle: func() error { session.SetMonitorMute(!session.Monitor().Muted); return nil },
			sync: func(ctx context.Context, d daw.DAW) error {
				return d.SetMonitorMute(ctx, session.Monitor().Muted)
			},
		},
	)
	tabs = append(tabs, mon)

	m.tabs = tabs
	for _, t := range tabs {
		for _, r := range t.rows {
			m.rows[r.label] = r
		}
	}
}

type connectedMsg struct {
	info daw.Info
	err  error
}

type syncedMsg struct {
	label string
	err   error
}

func (m studioModel) Init() tea.Cmd { return m.connect() }

// connect probes REAPER in the background so the mixer opens immediately
// rather than waiting on a workstation that may not be running.
func (m studioModel) connect() tea.Cmd {
	s := m.studio
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		info, err := s.dawc.Ping(c)
		return connectedMsg{info: info, err: err}
	}
}

// pushRow sends one row's value to REAPER without blocking the interface.
func (m studioModel) pushRow(r row) tea.Cmd {
	if !m.connected || r.sync == nil {
		return nil
	}
	// Already sending this row: note that it moved again and let the in-flight
	// call finish. The follow-up reads the value live, so it sends wherever
	// the row ended up rather than replaying every intermediate step.
	if m.pushing[r.label] {
		m.dirty[r.label] = true
		return nil
	}
	m.pushing[r.label] = true

	s := m.studio
	ctx := m.ctx
	label := r.label
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return syncedMsg{label: label, err: r.sync(c, s.dawc)}
	}
}

func (m studioModel) current() *tab { return &m.tabs[m.tabIdx] }

func (m studioModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case connectedMsg:
		if msg.err != nil {
			m.connected = false
			m.status = firstLine(msg.err.Error())
			m.statusErr = true
			return m, nil
		}
		m.connected = true
		m.dawName = strings.TrimSpace(msg.info.Name + " " + msg.info.Version)
		m.status = "connected"
		m.statusErr = false
		return m, nil

	case syncedMsg:
		m.pushing[msg.label] = false
		if msg.err != nil {
			m.status = msg.label + ": " + firstLine(msg.err.Error())
			m.statusErr = true
		}
		if m.dirty[msg.label] {
			m.dirty[msg.label] = false
			if r, ok := m.rows[msg.label]; ok {
				return m, m.pushRow(r)
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m studioModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.current().rows

	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit

	case "tab", "right shift", "l":
		m.tabIdx = (m.tabIdx + 1) % len(m.tabs)
		m.cursor = 0
		return m, nil
	case "shift+tab", "h":
		m.tabIdx = (m.tabIdx - 1 + len(m.tabs)) % len(m.tabs)
		m.cursor = 0
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
		return m, nil

	case "left", "right", "shift+left", "shift+right":
		if len(rows) == 0 {
			return m, nil
		}
		r := rows[m.cursor]
		if r.kind != rowLevel || r.adjust == nil {
			return m, nil
		}
		// Shift moves in larger steps, so crossing a wide range does not mean
		// holding a key down.
		step := studio.Level(1)
		if strings.HasPrefix(msg.String(), "shift+") {
			step = 3
		}
		if strings.HasSuffix(msg.String(), "left") {
			step = -step
		}
		change, err := r.adjust(step)
		if err != nil {
			m.status = firstLine(err.Error())
			m.statusErr = true
			return m, nil
		}
		if !change.Changed() {
			// Already at a limit; say so rather than appearing unresponsive.
			m.status = r.label + " is at its limit"
			m.statusErr = false
			return m, nil
		}
		m.status = fmt.Sprintf("%s  %s", r.label, change)
		m.statusErr = false
		return m, m.pushRow(r)

	case " ", "enter":
		if len(rows) == 0 {
			return m, nil
		}
		r := rows[m.cursor]
		if r.kind != rowToggle || r.toggle == nil {
			return m, nil
		}
		if err := r.toggle(); err != nil {
			m.status = firstLine(err.Error())
			m.statusErr = true
			return m, nil
		}
		m.status = r.label + ": " + r.value()
		m.statusErr = false
		return m, m.pushRow(r)

	case "s":
		if err := m.studio.store.Save(m.studio.session); err != nil {
			m.status = firstLine(err.Error())
			m.statusErr = true
		} else {
			m.status = "saved " + m.studio.session.Name
			m.statusErr = false
		}
		return m, nil

	case "r":
		m.status = "reconnecting..."
		m.statusErr = false
		return m, m.connect()
	}

	// A digit jumps to that tab.
	if key := msg.String(); len(key) == 1 {
		if n := int(key[0]) - '1'; n >= 0 && n < len(m.tabs) {
			m.tabIdx = n
			m.cursor = 0
		}
	}
	return m, nil
}

func (m studioModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString("  " + ui.Title.Render("STUDIO") + "  " +
		ui.Heading.Render(m.studio.session.Name))

	// Connection state belongs in the header: without it, a mixer that silently
	// changes nothing looks identical to one that is working.
	if m.connected {
		b.WriteString("   " + ui.OK.Render("● "+m.dawName))
	} else {
		b.WriteString("   " + ui.Err.Render("● REAPER not connected"))
	}
	b.WriteString("\n\n")

	b.WriteString("  ")
	for i, t := range m.tabs {
		label := fmt.Sprintf(" %d %s ", i+1, t.title)
		if i == m.tabIdx {
			b.WriteString(ui.Accent.Bold(true).Render(label))
		} else {
			b.WriteString(ui.Muted.Render(label))
		}
	}
	b.WriteString("\n\n")

	rows := m.current().rows
	labelWidth := 0
	for _, r := range rows {
		if n := len(r.label); n > labelWidth {
			labelWidth = n
		}
	}

	for i, r := range rows {
		marker := "   "
		label := ui.Pad(r.label, labelWidth)
		if i == m.cursor {
			marker = ui.Accent.Render(" ▸ ")
			label = ui.Accent.Bold(true).Render(label)
		}
		b.WriteString("  " + marker + label + "  ")

		switch r.kind {
		case rowLevel:
			b.WriteString(ui.Pad(r.value(), 9))
			b.WriteString(levelBar(r))
		case rowToggle:
			if r.on != nil && r.on() {
				b.WriteString(ui.OK.Render("● " + r.value()))
			} else {
				b.WriteString(ui.Muted.Render("○ " + r.value()))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.status != "" {
		if m.statusErr {
			b.WriteString("  " + ui.Warning(m.status) + "\n")
		} else {
			b.WriteString("  " + ui.Muted.Render(m.status) + "\n")
		}
	}

	b.WriteString("\n  " + ui.Muted.Render(
		"↑/↓ select · ←/→ adjust (shift for ±3) · space toggle · 1-6 tabs · s save · r reconnect · q quit") + "\n")

	// The mixer owns the screen while it runs, so it draws on the alternate
	// buffer and leaves the user's scrollback intact on exit.
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// levelBar draws a meter for a level row, with unity marked so the eye can
// find it without reading the number.
func levelBar(r row) string {
	const width = 28
	raw := strings.TrimSuffix(r.value(), " dB")
	var db studio.Level
	if raw == "-inf" {
		db = studio.MinLevel
	} else {
		var v float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(raw, "+"), "%g", &v); err != nil {
			return ""
		}
		db = studio.Level(v)
	}

	span := float64(studio.MaxSendLevel - studio.MinLevel)
	pos := int(float64(db-studio.MinLevel) / span * float64(width))
	unity := int(float64(studio.Unity-studio.MinLevel) / span * float64(width))

	var b strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i == pos:
			b.WriteString(ui.Accent.Render("┃"))
		case i == unity:
			b.WriteString(ui.Muted.Render("┆"))
		case i < pos:
			b.WriteString(ui.Muted.Render("─"))
		default:
			b.WriteString(ui.Muted.Render(" "))
		}
	}
	return b.String()
}
