package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/ui"
)

// nameWidth is the column the song title gets in the queue.
const nameWidth = 38

// selected returns the entry under the cursor, clamping the cursor first.
func (m *karaokeModel) selected() (queue.Entry, bool) {
	entries := m.store.Entries()
	if len(entries) == 0 {
		m.cursor = 0
		return queue.Entry{}, false
	}
	m.cursor = min(max(m.cursor, 0), len(entries)-1)
	return entries[m.cursor], true
}

func (m karaokeModel) focusInput() karaokeModel {
	m.inputFocus = true
	m.input.Focus()
	return m
}

func (m karaokeModel) focusList() karaokeModel {
	m.inputFocus = false
	m.input.Blur()
	return m
}

func (m karaokeModel) queueKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.confirmRemove != "" {
		id := m.confirmRemove
		m.confirmRemove = ""
		if key == "y" || key == "Y" {
			m.remove(id)
		}
		return m, nil
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	if m.inputFocus {
		switch key {
		case "enter":
			return m.addURLs(strings.Fields(m.input.Value()), true)
		case "tab", "down", "esc":
			return m.focusList(), nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	entries := m.store.Entries()
	entry, has := m.selected()
	switch key {
	case "q":
		return m.quit()
	case "?":
		m.showHelp = true
	case "tab":
		return m.focusInput(), nil
	case "up":
		if m.cursor == 0 {
			return m.focusInput(), nil
		}
		m.cursor--
	case "down":
		m.cursor = min(m.cursor+1, max(len(entries)-1, 0))
	case "shift+up", "K":
		if has {
			m.move(entry, -1)
		}
	case "shift+down", "J":
		if has {
			m.move(entry, 1)
		}
	case "d", "delete":
		if !has {
			break
		}
		if entry.State == queue.Preparing {
			m.confirmRemove = entry.ID
		} else {
			m.remove(entry.ID)
		}
	case "r":
		if !has {
			break
		}
		if entry.State != queue.Failed {
			m.setNotice(i18n.T("karaoke.tui.retry_only_failed"), true)
		} else if err := m.store.SetState(entry.ID, queue.Queued); err != nil {
			m.setNotice(err.Error(), true)
		} else {
			m.setNotice(i18n.Tf("karaoke.tui.retrying", entryName(entry)), false)
		}
	case "e":
		return m.exportSelected()
	case "c":
		if m.unsupported {
			m.setNotice(i18n.T("karaoke.doctor.unsupported"), true)
			break
		}
		m.screen = screenCalConfirm
		m.calWhy = false
	case "enter":
		return m.singSelected()
	}
	return m, nil
}

// paste adds every URL in pasted text. A single word is left for the input to
// take like typed text, so it can still be edited before Enter.
func (m karaokeModel) paste(content string) (tea.Model, tea.Cmd) {
	words := strings.Fields(content)
	if len(words) == 1 && m.inputFocus {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: content})
		return m, cmd
	}
	return m.addURLs(words, false)
}

// addURLs queues every word that is a URL and says what happened to the rest.
func (m karaokeModel) addURLs(words []string, clearInput bool) (tea.Model, tea.Cmd) {
	if len(words) == 0 {
		return m, nil
	}
	added := 0
	var problems []string
	for _, w := range words {
		if err := validateURL(w); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", w, err))
			continue
		}
		_, err := m.store.Add(w)
		var dup *queue.DuplicateError
		switch {
		case errors.As(err, &dup):
			problems = append(problems, i18n.Tf("karaoke.plain.duplicate", entryName(dup.Existing), dup.Existing.State))
		case err != nil:
			problems = append(problems, err.Error())
		default:
			added++
		}
	}
	if clearInput && len(problems) == 0 {
		m.input.Reset()
	}
	parts := problems
	if added > 0 {
		parts = append([]string{i18n.Tf("karaoke.tui.queued_n", added)}, problems...)
		m.cursor = max(len(m.store.Entries())-1, 0)
	}
	m.setNotice(strings.Join(parts, " · "), len(problems) > 0)
	return m, nil
}

func (m *karaokeModel) move(entry queue.Entry, delta int) {
	if err := m.store.Move(entry.ID, delta); err != nil {
		m.setNotice(err.Error(), true)
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.store.Entries())-1)
}

func (m *karaokeModel) remove(id string) {
	entry, _ := m.store.Get(id)
	if err := m.worker.Remove(id); err != nil {
		m.setNotice(err.Error(), true)
		return
	}
	delete(m.progress, id)
	m.setNotice(i18n.Tf("karaoke.tui.removed", entryName(entry)), false)
	m.selected() // re-clamp the cursor
}

// applyEvent folds a worker event into what the rows show. The store already
// holds the truth about state; the events add the live stage and fraction.
func (m *karaokeModel) applyEvent(ev queue.Event) {
	entry, ok := m.store.Get(ev.ID)
	if !ok {
		// The entry was removed while the event was in flight.
		delete(m.progress, ev.ID)
		return
	}
	switch ev.Kind {
	case queue.EventProgress:
		m.progress[ev.ID] = stageProgress{stage: ev.Stage, fraction: ev.Fraction}
	case queue.EventState:
		if ev.State != queue.Preparing {
			delete(m.progress, ev.ID)
		}
	case queue.EventWarning:
		m.setNotice(entryName(entry)+": "+ev.Warning, true)
	}
}

func (m karaokeModel) exportSelected() (tea.Model, tea.Cmd) {
	entry, ok := m.selected()
	if !ok {
		return m, nil
	}
	if entry.SongDir == "" {
		m.setNotice(i18n.Tf("karaoke.err.not_ready", entryName(entry)), true)
		return m, nil
	}
	m.setNotice(i18n.Tf("karaoke.tui.exporting", entryName(entry)), false)
	host, ctx := m.host, m.ctx
	return m, func() tea.Msg {
		res, err := host.Export(ctx, entry, "")
		return exportDoneMsg{entry: entry, res: res, err: err}
	}
}

// singSelected starts the selected song, or says why it cannot be sung yet.
func (m karaokeModel) singSelected() (tea.Model, tea.Cmd) {
	entry, ok := m.selected()
	anyReady := false
	for _, e := range m.store.Entries() {
		if e.State == queue.Ready || e.State == queue.Sung {
			anyReady = true
		}
	}
	switch {
	case !anyReady:
		m.setNotice(i18n.T("karaoke.tui.nothing_ready"), true)
		return m, nil
	case !ok || (entry.State != queue.Ready && entry.State != queue.Sung):
		m.setNotice(i18n.T("karaoke.tui.select_ready"), true)
		return m, nil
	case m.unsupported:
		m.setNotice(i18n.T("karaoke.doctor.unsupported"), true)
		return m, nil
	case m.calErr != nil:
		m.setNotice(m.calErr.Error(), true)
		return m, nil
	case m.cal == nil:
		m.setNotice(i18n.T("karaoke.tui.cal_checking"), false)
		return m, nil
	case !m.cal.Found:
		m.screen, m.calWhy = screenCalConfirm, true
		return m, nil
	}
	return m.startSing(entry)
}

func (m karaokeModel) calConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		return m.startCalibration()
	case "esc", "n", "q":
		m.screen = screenQueue
	}
	return m, nil
}

func (m karaokeModel) startCalibration() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	prog := &calProgress{}
	m.calProg, m.cancelCal, m.screen = prog, cancel, screenCalibrating
	m.worker.Pause() // the sweep needs a quiet machine and a quiet room
	host := m.host
	return m, func() tea.Msg {
		res, err := host.Calibrate(ctx, prog.set)
		if err != nil {
			return calDoneMsg{err: calibrateError(err)}
		}
		return calDoneMsg{err: host.SaveCalibration(res)}
	}
}

func (m karaokeModel) finishCalibration(msg calDoneMsg) (tea.Model, tea.Cmd) {
	if m.cancelCal != nil {
		m.cancelCal()
	}
	m.cancelCal, m.calProg, m.screen = nil, nil, screenQueue
	m.worker.Resume()
	switch {
	case errors.Is(msg.err, context.Canceled):
		m.setNotice(i18n.T("karaoke.cal.declined"), false)
	case msg.err != nil:
		m.setNotice(msg.err.Error(), true)
	default:
		m.setNotice(i18n.T("karaoke.tui.cal_saved"), false)
	}
	return m, m.loadCalibration()
}

func (m karaokeModel) calConfirmView() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.Heading.Render(i18n.T("karaoke.cal.confirm.title")) + "\n\n")
	if m.calWhy {
		b.WriteString("  " + i18n.T("karaoke.tui.needs_cal") + "\n\n")
	}
	b.WriteString("  " + ui.Warn.Render(i18n.T("karaoke.cal.warning")) + "\n\n")
	b.WriteString("  " + ui.Muted.Render(i18n.T("karaoke.tui.cal_keys")) + "\n")
	return b.String()
}

func (m karaokeModel) calibratingView() string {
	var stage, frac = m.calProg.get()
	label := i18n.T("karaoke.tui.cal_running")
	if stage != "" {
		label = i18n.T("karaoke.cal.stage." + string(stage))
	}
	return "\n  " + m.spinner() + " " + label + "\n\n  " + bar(frac, 40) + "\n\n  " +
		ui.Muted.Render(i18n.T("karaoke.tui.cal_running_keys")) + "\n"
}

func (m karaokeModel) queueView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	if m.banner != "" {
		b.WriteString("\n  " + ui.Warning(m.banner) + "\n")
	}
	b.WriteString("\n  " + m.input.View() + "\n\n")

	entries := m.store.Entries()
	if len(entries) == 0 {
		b.WriteString("  " + ui.Muted.Render(i18n.T("karaoke.tui.empty")) + "\n")
	}
	for i, e := range entries {
		b.WriteString(m.rowView(e, i == m.cursor && !m.inputFocus) + "\n")
	}

	if m.notice != "" {
		style := ui.OK
		if m.noticeErr {
			style = ui.Warn
		}
		b.WriteString("\n  " + style.Render(m.notice) + "\n")
	}
	switch {
	case m.confirmRemove != "":
		en, _ := m.store.Get(m.confirmRemove)
		b.WriteString("\n  " + ui.Warning(i18n.Tf("karaoke.tui.confirm_remove", entryName(en))) + "\n")
	case m.showHelp:
		b.WriteString("\n" + m.helpView())
	default:
		b.WriteString("\n  " + ui.Muted.Render(i18n.T("karaoke.tui.keys_queue")) + "\n")
	}
	return b.String()
}

func (m karaokeModel) helpView() string {
	var b strings.Builder
	for _, line := range strings.Split(i18n.T("karaoke.tui.help"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// rowView is one queue line: cursor, state glyph, title and the detail that
// fits the state.
func (m karaokeModel) rowView(e queue.Entry, selected bool) string {
	marker := "  "
	if selected {
		marker = ui.Accent.Render("▸ ")
	}
	var glyph, detail string
	switch e.State {
	case queue.Queued:
		glyph = ui.Muted.Render("○")
	case queue.Preparing:
		glyph = ui.Accent.Render(m.spinner())
		p := m.progress[e.ID]
		stage := p.stage
		if stage == "" {
			stage = e.Stage
		}
		if stage != "" {
			detail = stage.Label() + " " + bar(p.fraction, 10) + fmt.Sprintf(" %3d%%", int(p.fraction*100+0.5))
		}
	case queue.Ready:
		glyph = ui.OK.Render("●")
	case queue.Failed:
		glyph = ui.Err.Render(ui.GlyphFail)
		detail = ui.Err.Render(kTrunc(e.Error, max(m.width-nameWidth-8, 10)))
	case queue.Sung:
		glyph = ui.Accent.Render("★")
	}
	if n := len(e.Scores); n > 0 {
		last := e.Scores[n-1]
		parts := make([]string, len(last.Players))
		for i, p := range last.Players {
			parts[i] = fmt.Sprintf("%s %d", p.Name, p.Score)
		}
		detail = ui.OK.Render(strings.Join(parts, " · "))
	}
	name := ui.Pad(kTrunc(entryName(e), nameWidth), nameWidth)
	if selected {
		name = ui.Heading.Render(name)
	}
	return fmt.Sprintf("  %s%s %s  %s", marker, glyph, name, detail)
}
