package cli

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/ui"
)

// singFrame is how often the sing view reads the session: about 30 fps.
const singFrame = 33 * time.Millisecond

// The pitch lane is a fixed-width band centred on the target: laneSemitones
// either side, laneColsPerSemitone cells for each, and one cell for the centre.
const (
	laneSemitones       = 6
	laneColsPerSemitone = 2
	laneCentre          = laneSemitones * laneColsPerSemitone
	laneWidth           = 2*laneCentre + 1
	levelWidth          = 10
)

type singState struct {
	entry queue.Entry
	sess  karaokeSession
	lines []lyrics.Line
	snap  live.Snapshot

	// finishing is set once the end is known and the results are being fetched.
	finishing bool
	// confirmStop shows the "stop early?" question; autoPaused says the song
	// was running when it was asked, so answering no should resume it.
	confirmStop bool
	autoPaused  bool
	// workerPaused says this view paused the preparation worker.
	workerPaused bool
}

type (
	sessionStartedMsg struct {
		entry queue.Entry
		sess  karaokeSession
		lines []lyrics.Line
		err   error
	}
	singTickMsg    struct{}
	sessionDoneMsg struct {
		entry queue.Entry
		sess  karaokeSession
		res   live.Results
		err   error
	}
)

func (m karaokeModel) startSing(entry queue.Entry) (tea.Model, tea.Cmd) {
	m.sing = singState{entry: entry}
	m.screen = screenSing
	if m.cfg.PausePrepWhileSinging {
		m.worker.Pause()
		m.sing.workerPaused = true
	}
	host, ctx := m.host, m.ctx
	return m, func() tea.Msg {
		sess, err := host.StartSession(ctx, entry, nil)
		if err != nil {
			return sessionStartedMsg{entry: entry, err: err}
		}
		return sessionStartedMsg{entry: entry, sess: sess, lines: host.Lyrics(entry)}
	}
}

// leaveSing gives the CPU back to the preparation worker.
func (m *karaokeModel) leaveSing() {
	if m.sing.workerPaused {
		m.worker.Resume()
	}
	m.sing = singState{}
}

func (m karaokeModel) sessionStarted(msg sessionStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.leaveSing()
		m.screen = screenQueue
		switch {
		case errors.Is(msg.err, errNotCalibrated):
			m.screen, m.calWhy = screenCalConfirm, true
		case errors.Is(msg.err, audioio.ErrUnsupported):
			m.setNotice(i18n.T("karaoke.doctor.unsupported"), true)
		default:
			m.setNotice(i18n.Tf("karaoke.tui.sing_failed", msg.err), true)
		}
		return m, nil
	}
	if m.quitting || m.screen != screenSing {
		_ = msg.sess.Close()
		return m, nil
	}
	m.sing.sess, m.sing.lines = msg.sess, msg.lines
	m.sing.snap = msg.sess.Snapshot()
	return m, singTick()
}

func singTick() tea.Cmd {
	return tea.Tick(singFrame, func(time.Time) tea.Msg { return singTickMsg{} })
}

func (m karaokeModel) singTick(singTickMsg) (tea.Model, tea.Cmd) {
	s := &m.sing
	if m.screen != screenSing || s.sess == nil || s.finishing {
		return m, nil
	}
	s.snap = s.sess.Snapshot()
	if s.snap.Done {
		s.finishing = true
		return m, waitSession(s.entry, s.sess, false)
	}
	return m, singTick()
}

// waitSession collects the results; stop ends the song first.
func waitSession(entry queue.Entry, sess karaokeSession, stop bool) tea.Cmd {
	return func() tea.Msg {
		var res live.Results
		var err error
		if stop {
			res, err = sess.Stop()
		} else {
			res, err = sess.Wait()
		}
		return sessionDoneMsg{entry: entry, sess: sess, res: res, err: err}
	}
}

func (m karaokeModel) singKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := &m.sing
	if s.sess == nil || s.finishing {
		return m, nil
	}
	key := msg.String()
	if s.confirmStop {
		switch key {
		case "y", "Y", "enter":
			s.confirmStop, s.finishing = false, true
			return m, waitSession(s.entry, s.sess, true)
		default:
			s.confirmStop = false
			if s.autoPaused {
				s.sess.Resume()
				s.snap = s.sess.Snapshot()
			}
		}
		return m, nil
	}
	switch key {
	case " ", "space":
		if s.snap.Paused {
			s.sess.Resume()
		} else {
			s.sess.Pause()
		}
		s.snap = s.sess.Snapshot()
	case "esc", "q":
		s.confirmStop, s.autoPaused = true, !s.snap.Paused
		if s.autoPaused {
			s.sess.Pause()
			s.snap = s.sess.Snapshot()
		}
	}
	return m, nil
}

func (m karaokeModel) sessionDone(msg sessionDoneMsg) (tea.Model, tea.Cmd) {
	if msg.sess != nil {
		_ = msg.sess.Close()
	}
	lines := m.sing.lines
	m.leaveSing()
	if msg.err != nil && len(msg.res.Players) == 0 {
		m.screen = screenQueue
		m.setNotice(i18n.Tf("karaoke.tui.sing_failed", msg.err), true)
		return m, nil
	}
	m.results = resultsState{entry: msg.entry, res: msg.res, lines: lines}
	m.results.saveErr = m.recordScore(msg.entry, msg.res)
	m.screen = screenResults
	return m, nil
}

// laneFold is the sung pitch's offset from the target in semitones, folded by
// octaves into [-6, 6]: singing an octave away is the same note.
func laneFold(sung, target float64) float64 {
	d := sung - target
	return d - 12*math.Round(d/12)
}

// pitchLane draws where a singer is against the target: a band for the target
// pitch and a marker for the sung one, green on a hit and red on a miss. There
// is no marker when the singer is unvoiced, and nothing to compare against
// when the original melody is silent.
func pitchLane(p live.PlayerSnapshot) string {
	cells := make([]string, laneWidth)
	for i := range cells {
		cells[i] = ui.Muted.Render("·")
	}
	if p.TargetVoiced {
		for c := laneCentre - 1; c <= laneCentre+1; c++ {
			cells[c] = ui.Accent.Render("▒")
		}
		if p.Voiced {
			col := laneCentre + int(math.Round(laneFold(p.SungMIDI, p.TargetMIDI)*laneColsPerSemitone))
			col = min(max(col, 0), laneWidth-1)
			style := ui.Accent
			switch p.Last {
			case score.EventHit:
				style = ui.OK
			case score.EventMiss:
				style = ui.Err
			}
			cells[col] = style.Render("●")
		}
	}
	return strings.Join(cells, "")
}

func (m karaokeModel) singView() string {
	s := m.sing
	var b strings.Builder
	title := entryName(s.entry)
	b.WriteString("\n  " + ui.Heading.Render(kTrunc(title, m.width-4)) + "\n")
	if s.sess == nil {
		b.WriteString("\n  " + m.spinner() + " " + ui.Muted.Render(i18n.T("karaoke.tui.starting")) + "\n")
		return b.String()
	}
	snap := s.snap

	frac := 0.0
	if snap.Duration > 0 {
		frac = float64(snap.Position) / float64(snap.Duration)
	}
	b.WriteString("  " + ui.Muted.Render(clock(snap.Position)) + " " + bar(frac, 40) + " " +
		ui.Muted.Render(clock(snap.Duration)))
	if snap.Paused {
		b.WriteString("  " + ui.Warn.Render(i18n.T("karaoke.tui.paused")))
	}
	b.WriteString("\n\n")

	cur, next := m.lyricLines(snap)
	b.WriteString("  " + cur + "\n  " + ui.Muted.Render(next) + "\n\n")

	nameW := 0
	for _, p := range snap.Players {
		nameW = max(nameW, len([]rune(p.Name)))
	}
	for _, p := range snap.Players {
		b.WriteString(fmt.Sprintf("  %s %s %s %s  %s\n",
			ui.Heading.Render(ui.Pad(p.Name, nameW)),
			ui.OK.Bold(true).Render(fmt.Sprintf("%5d", p.Score)),
			ui.Muted.Render(ui.Pad(i18n.Tf("karaoke.tui.streak", fmt.Sprintf("%.1fs", p.Streak.Seconds())), 14)),
			pitchLane(p),
			meterBar(daw.Meter{Peak: p.LevelDBFS}, levelWidth)))
	}

	if len(snap.Warnings) > 0 {
		b.WriteString("\n  " + ui.Warning(kTrunc(strings.Join(snap.Warnings, " · "), m.width-6)) + "\n")
	}
	if s.confirmStop {
		b.WriteString("\n  " + ui.Warning(i18n.T("karaoke.tui.stop_confirm")) + "\n")
	} else {
		b.WriteString("\n  " + ui.Muted.Render(i18n.T("karaoke.tui.keys_sing")) + "\n")
	}
	return b.String()
}

// lyricLines returns the current line, with the sung part highlighted, and the
// one after it. Before the first line the current one is empty and the next is
// the upcoming line.
func (m karaokeModel) lyricLines(snap live.Snapshot) (cur, next string) {
	lines := m.sing.lines
	from := 0
	if i := snap.LineIndex; i >= 0 && i < len(lines) {
		r := []rune(lines[i].Text)
		n := min(max(int(snap.LineProgress*float64(len(r))+0.5), 0), len(r))
		cur = ui.Accent.Bold(true).Render(string(r[:n])) + ui.Heading.Render(string(r[n:]))
		from = i + 1
	} else {
		for from < len(lines) && lines[from].Start <= snap.Position {
			from++
		}
	}
	for j := from; j < len(lines); j++ {
		if !lines[j].Blank() {
			return cur, lines[j].Text
		}
	}
	return cur, ""
}
