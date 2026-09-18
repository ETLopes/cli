package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/i18n"
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
	// note is a short caveat shown beside the control, for effects that will
	// not do anything audible until configured.
	note string
	kind rowKind
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
	// The console is a grid rather than a list, so it tracks its own cursor:
	// a column per input and a row per control.
	conRows []consoleRow
	conChan int
	conRow  int

	// The patch page edits a working copy: changing which input an
	// instrument uses is a change to the shape of the studio, so it takes a
	// deliberate save rather than applying as you scroll.
	patch      []patchEntry
	patchRow   int
	patchDirty bool
	// showHelp keeps the explanation of the selected control on screen. On by
	// default, since not knowing what a control does is the common case.
	showHelp bool

	// fxInstrument is which instrument the FX page is showing. The chains run
	// to dozens of pedals, so they are paged per instrument rather than
	// listed end to end.
	fxInstrument int
	// offset is the first visible row, so a long chain scrolls instead of
	// running off the screen.
	offset int
	height int

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
		ctx: ctx, studio: s, width: 90, height: 30,
		pushing: map[string]bool{},
		dirty:   map[string]bool{},
		rows:    map[string]row{},
	}
	m.conRows = consoleRows()
	m.patch = patchEntries()
	m.showHelp = true
	m.rebuild()
	return m
}

// onConsole reports whether the console page is showing.
func (m studioModel) onConsole() bool { return m.current().title == consoleTitle }

// consoleTitle names the console page.
const consoleTitle = "CONSOLE"

// patchTitle names the input patching page.
const patchTitle = "INPUTS"

// onPatch reports whether the patch page is showing.
func (m studioModel) onPatch() bool { return m.current().title == patchTitle }

// rebuild constructs the tab contents from the session. Rows close over the
// session, so they always read live values rather than a stale copy.
func (m *studioModel) rebuild() {
	session := m.studio.session
	tabs := []tab{{title: consoleTitle}, {title: patchTitle}}

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
	if withChains := studio.InstrumentsWithChains(); len(withChains) > 0 {
		if m.fxInstrument >= len(withChains) {
			m.fxInstrument = 0
		}
		id := withChains[m.fxInstrument]
		for _, e := range studio.Chain(id) {
			instID, eff := id, e
			fx.rows = append(fx.rows, row{
				label: eff.Name,
				note:  effectNote(eff),
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

// truncate shortens a line to fit the terminal, measuring display width so a
// styled string is not cut mid-escape.
func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width || width < 2 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

// effectNote explains, briefly, why an effect might not appear to do anything
// once switched on. Silence on that point is what sent the last hour into
// diagnosing a tuner that was working the whole time.
func effectNote(e studio.Effect) string {
	switch {
	case e.NeedsIR:
		return "loads clean until you load a cab impulse response"
	}
	return ""
}

// fxInstruments lists the instruments the FX page can show.
func fxInstruments() []string { return studio.InstrumentsWithChains() }

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

// visibleRows is how many controls fit on screen, once the header, the tab
// bar, the status line and the key hints have taken their share.
func (m studioModel) visibleRows() int {
	// A tab bar wide enough to wrap costs a line for every row past the first.
	chrome := 12 + max(1, len(tabLayout(m.tabs, m.width))) - 1
	if n := m.height - chrome; n > 3 {
		return n
	}
	return 3
}

// tabIndent is the left margin every page shares.
const tabIndent = "  "

// tabLabel is how one page reads in the tab bar. Only the first nine are
// numbered, because only those nine have a digit that jumps to them.
func tabLabel(t tab, i int) string {
	if i < 9 {
		return fmt.Sprintf(" %d %s ", i+1, t.title)
	}
	return " " + t.title + " "
}

// tabLayout groups the pages into the lines they occupy at a given width,
// each line holding the indices of the tabs on it. Eight cue pages do not fit
// beside the others on a narrow terminal, so the bar wraps rather than
// spilling off the edge.
func tabLayout(tabs []tab, width int) [][]int {
	avail := max(20, width-len(tabIndent))
	var lines [][]int
	var line []int
	used := 0
	for i, t := range tabs {
		w := lipgloss.Width(tabLabel(t, i))
		if len(line) > 0 && used+w > avail {
			lines = append(lines, line)
			line, used = nil, 0
		}
		line = append(line, i)
		used += w
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	return lines
}

// renderTabs draws the tab bar, highlighting the active page.
func renderTabs(tabs []tab, active, width int) string {
	var b strings.Builder
	for _, line := range tabLayout(tabs, width) {
		b.WriteString(tabIndent)
		for _, i := range line {
			label := tabLabel(tabs[i], i)
			if i == active {
				b.WriteString(ui.Accent.Bold(true).Render(label))
			} else {
				b.WriteString(ui.Muted.Render(label))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// scrollToCursor keeps the selected row on screen.
func (m *studioModel) scrollToCursor() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m studioModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scrollToCursor()
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
			// A console label carries its own coordinates.
			if inst, row, ok := parseConsoleLabel(msg.label, m.conRows); ok {
				return m, m.pushConsole(inst, row)
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
	}

	if m.onConsole() {
		return m.handleConsoleKey(msg)
	}
	if m.onPatch() {
		return m.handlePatchKey(msg)
	}

	switch msg.String() {

	case "tab", "l":
		m.tabIdx = (m.tabIdx + 1) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil
	case "shift+tab", "h":
		m.tabIdx = (m.tabIdx - 1 + len(m.tabs)) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		m.scrollToCursor()
		return m, nil
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
		m.scrollToCursor()
		return m, nil
	case "pgup":
		m.cursor = max(0, m.cursor-m.visibleRows())
		m.scrollToCursor()
		return m, nil
	case "pgdown":
		m.cursor = min(len(rows)-1, m.cursor+m.visibleRows())
		m.scrollToCursor()
		return m, nil

	case "left", "right", "shift+left", "shift+right":
		// On the FX page nothing is a level, so the same keys page between
		// instruments instead of going unused.
		if m.current().title == "FX" {
			insts := fxInstruments()
			if len(insts) > 0 {
				if strings.HasSuffix(msg.String(), "left") {
					m.fxInstrument = (m.fxInstrument - 1 + len(insts)) % len(insts)
				} else {
					m.fxInstrument = (m.fxInstrument + 1) % len(insts)
				}
				m.cursor, m.offset = 0, 0
				m.rebuild()
			}
			return m, nil
		}
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
			m.status = i18n.Tf("console.limit", r.label)
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

// handleConsoleKey drives the desk. Arrows move around the grid and the value
// keys change what is under the cursor, because left and right are already
// spent on choosing a channel.
func (m studioModel) handleConsoleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	instruments := studio.Instruments()
	if len(instruments) == 0 || len(m.conRows) == 0 {
		return m, nil
	}

	notches := 0
	switch msg.String() {
	case "left", "h":
		if m.conChan > 0 {
			m.conChan--
		}
		return m, nil
	case "right", "l":
		if m.conChan < len(instruments)-1 {
			m.conChan++
		}
		return m, nil
	case "up", "k":
		if m.conRow > 0 {
			m.conRow--
		}
		return m, nil
	case "down", "j":
		if m.conRow < len(m.conRows)-1 {
			m.conRow++
		}
		return m, nil
	case "tab":
		m.tabIdx = (m.tabIdx + 1) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil
	case "shift+tab":
		m.tabIdx = (m.tabIdx - 1 + len(m.tabs)) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil
	case "s":
		if err := m.studio.store.Save(m.studio.session); err != nil {
			m.status, m.statusErr = firstLine(err.Error()), true
		} else {
			m.status, m.statusErr = i18n.Tf("console.saved", m.studio.session.Name), false
		}
		return m, nil
	case "r":
		m.status, m.statusErr = i18n.T("console.connecting"), false
		return m, m.connect()
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "+", "=":
		notches = 1
	case "-", "_":
		notches = -1
	case "shift+up", "pgup":
		notches = 4
	case "shift+down", "pgdown":
		notches = -4
	case " ", "enter":
		inst := instruments[m.conChan].ID
		row := m.conRows[m.conRow]
		msgText, err := toggleConsole(m.studio.session, inst, row)
		if err != nil {
			m.status, m.statusErr = firstLine(err.Error()), true
			return m, nil
		}
		if msgText == "" {
			return m, nil
		}
		m.status, m.statusErr = instruments[m.conChan].Name+" "+msgText, false
		return m, m.pushConsole(inst, row)
	}

	if notches == 0 {
		return m, nil
	}
	inst := instruments[m.conChan].ID
	row := m.conRows[m.conRow]
	if row.isSwitch() {
		return m, nil
	}
	msgText, err := adjustConsole(m.studio.session, inst, row, notches)
	if err != nil {
		m.status, m.statusErr = firstLine(err.Error()), true
		return m, nil
	}
	m.status, m.statusErr = instruments[m.conChan].Name+" "+msgText, false
	return m, m.pushConsole(inst, row)
}

// pushConsole sends one strip to the workstation, coalescing the way the list
// pages do so a held key cannot queue a call per press.
func (m studioModel) pushConsole(instrumentID string, r consoleRow) tea.Cmd {
	if !m.connected {
		return nil
	}
	label := instrumentID + "/" + r.label()
	if m.pushing[label] {
		m.dirty[label] = true
		return nil
	}
	m.pushing[label] = true

	st, ctx := m.studio, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return syncedMsg{label: label, err: syncConsole(c, st.dawc, st.session, instrumentID, r)}
	}
}

// handlePatchKey drives the input assignment.
func (m studioModel) handlePatchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.patch) == 0 {
		return m, nil
	}
	e := &m.patch[m.patchRow]

	switch msg.String() {
	case "up", "k":
		if m.patchRow > 0 {
			m.patchRow--
		}
		return m, nil
	case "down", "j":
		if m.patchRow < len(m.patch)-1 {
			m.patchRow++
		}
		return m, nil
	case "left", "-":
		if e.Channel > 1 {
			e.Channel--
			m.patchDirty = true
		}
		return m, nil
	case "right", "+", "=":
		limit := maxInput
		if e.Stereo {
			limit = maxInput - 1
		}
		if e.Channel < limit {
			e.Channel++
			m.patchDirty = true
		}
		return m, nil
	case "m", " ":
		e.Stereo = !e.Stereo
		if e.Stereo && e.Channel >= maxInput {
			e.Channel = maxInput - 1
		}
		m.patchDirty = true
		return m, nil
	case "tab":
		m.tabIdx = (m.tabIdx + 1) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil
	case "shift+tab":
		m.tabIdx = (m.tabIdx - 1 + len(m.tabs)) % len(m.tabs)
		m.cursor, m.offset = 0, 0
		return m, nil
	case "s":
		for i := range m.patch {
			if patchConflict(m.patch, i) != "" {
				m.status, m.statusErr = i18n.T("patch.conflicts"), true
				return m, nil
			}
		}
		if err := savePatch(m.patch); err != nil {
			m.status, m.statusErr = firstLine(err.Error()), true
			return m, nil
		}
		m.patchDirty = false
		m.status, m.statusErr = i18n.T("patch.saved"), false
		// The console follows the rig, so it is rebuilt around the new one.
		m.conRows = consoleRows()
		m.conChan, m.conRow = 0, 0
		m.rebuild()
		return m, m.applyTopology()
	}
	return m, nil
}

// applyTopology pushes the new assignment to REAPER, which is what actually
// moves a track onto a different input.
func (m studioModel) applyTopology() tea.Cmd {
	if !m.connected {
		return nil
	}
	st, ctx := m.studio, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err := st.dawc.Setup(c)
		return syncedMsg{label: patchTitle, err: err}
	}
}

func (m studioModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString("  " + ui.Title.Render("STUDIO") + "  " +
		ui.Heading.Render(m.studio.session.Name))

	// Connection state belongs in the header: without it, a mixer that
	// silently changes nothing looks identical to one that is working.
	if m.connected {
		b.WriteString("   " + ui.OK.Render("● "+m.dawName))
	} else {
		b.WriteString("   " + ui.Err.Render("● "+i18n.T("studio.disconnected")))
	}
	b.WriteString("\n\n")

	b.WriteString(renderTabs(m.tabs, m.tabIdx, m.width))

	onFX := m.current().title == "FX"
	if onFX {
		b.WriteString("\n  ")
		for i, id := range fxInstruments() {
			inst, _ := studio.LookupInstrument(id)
			name := " " + inst.Name + " "
			if i == m.fxInstrument {
				b.WriteString(ui.Accent.Bold(true).Render(name))
			} else {
				b.WriteString(ui.Muted.Render(name))
			}
		}
		b.WriteString("   " + ui.Muted.Render("←/→"))
	}
	b.WriteString("\n\n")

	if m.onPatch() {
		b.WriteString(renderPatch(m.patch, m.patchRow, m.width))
		if m.patchDirty {
			b.WriteString("\n  " + ui.Warn.Render("● "+i18n.T("patch.unsaved")) + "\n")
		}
		if m.status != "" {
			if m.statusErr {
				b.WriteString("\n  " + ui.Warning(truncate(m.status, max(20, m.width-4))) + "\n")
			} else {
				b.WriteString("\n  " + ui.Success(m.status) + "\n")
			}
		}
		b.WriteString("\n  " + ui.Muted.Render(i18n.T("patch.keys")) + "\n")
		v := tea.NewView(b.String())
		v.AltScreen = true
		return v
	}

	if m.onConsole() {
		b.WriteString(renderConsole(m.studio.session, m.conRows, m.conChan, m.conRow, m.width))

		// What the selected control does, always in view. A desk assumes you
		// already know; there is no reason this should.
		if m.conRow < len(m.conRows) && m.showHelp {
			row := m.conRows[m.conRow]
			b.WriteString("\n  " + ui.Accent.Render(row.label()) + "  ")
			for i, line := range wrap(row.explain(), max(40, m.width-12)) {
				if i > 0 {
					b.WriteString("\n        ")
				}
				b.WriteString(ui.Muted.Render(line))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		if m.status != "" {
			if m.statusErr {
				b.WriteString("  " + ui.Warning(truncate(m.status, max(20, m.width-4))) + "\n")
			} else {
				b.WriteString("  " + ui.Muted.Render(truncate(m.status, max(20, m.width-4))) + "\n")
			}
		}
		b.WriteString("\n  " + ui.Muted.Render(
			i18n.T("console.keys")) + "\n")
		v := tea.NewView(b.String())
		v.AltScreen = true
		return v
	}

	rows := m.current().rows
	labelWidth := 0
	for _, r := range rows {
		if n := lipgloss.Width(r.label); n > labelWidth {
			labelWidth = n
		}
	}

	visible := m.visibleRows()
	first, last := m.offset, min(m.offset+visible, len(rows))

	if first > 0 {
		b.WriteString("  " + ui.Muted.Render(fmt.Sprintf("     ↑ %d more", first)) + "\n")
	}

	for i := first; i < last; i++ {
		r := rows[i]
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
				b.WriteString(ui.OK.Render("● " + ui.Pad(r.value(), 4)))
			} else {
				b.WriteString(ui.Muted.Render("○ " + ui.Pad(r.value(), 4)))
			}
			// A caveat only matters for something switched on that still
			// appears to do nothing.
			if r.note != "" && r.on != nil && r.on() {
				b.WriteString("  " + ui.Warn.Render(r.note))
			} else if r.note != "" {
				b.WriteString("  " + ui.Muted.Render(r.note))
			}
		}
		b.WriteString("\n")
	}

	if last < len(rows) {
		b.WriteString("  " + ui.Muted.Render(fmt.Sprintf("     ↓ %d more", len(rows)-last)) + "\n")
	}

	b.WriteString("\n")
	if m.status != "" {
		if m.statusErr {
			b.WriteString("  " + ui.Warning(truncate(m.status, max(20, m.width-4))) + "\n")
		} else {
			b.WriteString("  " + ui.Muted.Render(truncate(m.status, max(20, m.width-4))) + "\n")
		}
	}

	hint := "↑/↓ select · ←/→ adjust · space toggle · tab page · s save · r reconnect · q quit"
	if onFX {
		hint = "↑/↓ select · ←/→ instrument · space toggle · tab page · s save · q quit"
	}
	b.WriteString("\n  " + ui.Muted.Render(hint) + "\n")

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
