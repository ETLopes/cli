package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/song"
	"github.com/ETLopes/cli/internal/karaoke/ultrastar"
	"github.com/ETLopes/cli/internal/ui"
)

// The karaoke screen is one Bubble Tea program with four views: the queue
// (home), the calibration flow, the sing view and the results. Everything it
// needs from the outside world comes through three small interfaces, so the
// tests drive it with scripted collaborators and the integration test with the
// real ones over a fake audio device.

// karaokeHost is what the screen asks of the application.
type karaokeHost interface {
	Calibration() (CalibrationStatus, error)
	Calibrate(ctx context.Context, progress func(calibrate.Stage, float64)) (calibrate.Result, error)
	SaveCalibration(calibrate.Result) error
	StartSession(ctx context.Context, entry queue.Entry, players []config.KaraokeInput) (karaokeSession, error)
	Lyrics(entry queue.Entry) []lyrics.Line
	Export(ctx context.Context, entry queue.Entry, outDir string) (ultrastar.Result, error)
}

// karaokeSession is the part of *live.Session the sing view uses.
type karaokeSession interface {
	Snapshot() live.Snapshot
	Pause()
	Resume()
	Stop() (live.Results, error)
	Wait() (live.Results, error)
	Close() error
}

// karaokeWorker is the part of *queue.Worker the screen uses.
type karaokeWorker interface {
	Pause()
	Resume()
	Remove(id string) error
	CancelCurrent()
}

// appHost adapts the production karaokeApp to karaokeHost.
type appHost struct{ *karaokeApp }

func (h appHost) StartSession(ctx context.Context, entry queue.Entry, players []config.KaraokeInput) (karaokeSession, error) {
	s, err := h.karaokeApp.StartSession(ctx, entry, players)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Lyrics returns the synced lines of entry's song; a song without them shows
// none, which the sing view handles.
func (h appHost) Lyrics(entry queue.Entry) []lyrics.Line {
	sg, err := song.Load(entry.SongDir)
	if err != nil {
		return nil
	}
	l, ok, err := sg.LoadLyrics()
	if err != nil || !ok {
		return nil
	}
	return l.Lines
}

type karaokeScreen int

const (
	screenQueue karaokeScreen = iota
	screenCalConfirm
	screenCalibrating
	screenSing
	screenResults
)

// runKaraokeTUI opens the karaoke screen and keeps the background worker
// running inside it. The program exits before this returns, and the worker is
// stopped last, so the queue on disk is always consistent.
var runKaraokeTUI = func(ctx context.Context, app *karaokeApp, urls []string) error {
	store, warn, err := app.OpenQueue()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker := app.NewWorker(store)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = worker.Run(runCtx)
	}()
	defer func() { cancel(); <-done }()

	m := newKaraokeModel(runCtx, appHost{app}, store, worker, worker.Events(), app.cfg)
	m.initialURLs = urls
	if warn != nil {
		m.banner = i18n.Tf("karaoke.tui.queue_reset", warn.MovedTo)
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// stageProgress is what the worker last said about the entry it is preparing.
type stageProgress struct {
	stage    song.Stage
	fraction float64
}

// calProgress is shared between the calibration goroutine and the view.
type calProgress struct {
	mu       sync.Mutex
	stage    calibrate.Stage
	fraction float64
}

func (c *calProgress) set(s calibrate.Stage, f float64) {
	c.mu.Lock()
	c.stage, c.fraction = s, f
	c.mu.Unlock()
}

func (c *calProgress) get() (calibrate.Stage, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stage, c.fraction
}

type karaokeModel struct {
	ctx    context.Context
	host   karaokeHost
	store  *queue.Store
	worker karaokeWorker
	events <-chan queue.Event
	cfg    config.Karaoke
	now    func() time.Time

	width       int
	screen      karaokeScreen
	showHelp    bool
	quitting    bool
	spin        int
	initialURLs []string

	// Queue view.
	input         textinput.Model
	inputFocus    bool
	cursor        int
	progress      map[string]stageProgress
	notice        string
	noticeErr     bool
	banner        string
	confirmRemove string
	// calWhy adds the explanation to the calibration question when it was
	// reached by trying to sing.
	calWhy bool

	// Calibration.
	cal         *CalibrationStatus
	calErr      error
	unsupported bool
	calProg     *calProgress
	cancelCal   context.CancelFunc

	sing    singState
	results resultsState
}

func newKaraokeModel(ctx context.Context, host karaokeHost, store *queue.Store, worker karaokeWorker,
	events <-chan queue.Event, cfg config.Karaoke) karaokeModel {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = i18n.T("karaoke.tui.input_placeholder")
	in.SetWidth(60)
	m := karaokeModel{
		ctx: ctx, host: host, store: store, worker: worker, events: events, cfg: cfg,
		now: time.Now, width: 80, input: in, progress: map[string]stageProgress{},
	}
	// A queue with songs opens on the list, so the single-letter keys work at
	// once; an empty one opens on the input, which is all there is to do.
	m.inputFocus = len(store.Entries()) == 0
	if m.inputFocus {
		m.input.Focus()
	}
	return m
}

// Messages.
type (
	tickMsg         struct{}
	workerEventMsg  struct{ ev queue.Event }
	workerClosedMsg struct{}
	calStatusMsg    struct {
		status CalibrationStatus
		err    error
	}
	calDoneMsg    struct{ err error }
	exportDoneMsg struct {
		entry queue.Entry
		res   ultrastar.Result
		err   error
	}
)

func (m karaokeModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitEvent(), m.loadCalibration(), m.tick(), textinput.Blink}
	if len(m.initialURLs) > 0 {
		urls := m.initialURLs
		cmds = append(cmds, func() tea.Msg { return tea.PasteMsg{Content: strings.Join(urls, "\n")} })
	}
	return tea.Batch(cmds...)
}

func (m karaokeModel) tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m karaokeModel) waitEvent() tea.Cmd {
	events, ctx := m.events, m.ctx
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				return workerClosedMsg{}
			}
			return workerEventMsg{ev}
		case <-ctx.Done():
			return workerClosedMsg{}
		}
	}
}

func (m karaokeModel) loadCalibration() tea.Cmd {
	return func() tea.Msg {
		st, err := m.host.Calibration()
		return calStatusMsg{status: st, err: err}
	}
}

func (m karaokeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 40)
		return m, nil
	case tickMsg:
		m.spin++
		return m, m.tick()
	case workerEventMsg:
		m.applyEvent(msg.ev)
		return m, m.waitEvent()
	case workerClosedMsg:
		m.events = nil
		return m, nil
	case calStatusMsg:
		m.cal, m.calErr = nil, msg.err
		if msg.err == nil {
			st := msg.status
			m.cal = &st
		}
		m.unsupported = errors.Is(msg.err, audioio.ErrUnsupported)
		return m, nil
	case exportDoneMsg:
		if msg.err != nil {
			m.setNotice(i18n.Tf("karaoke.tui.export_failed", msg.err), true)
		} else {
			m.setNotice(i18n.Tf("karaoke.tui.exported", msg.res.Dir), false)
		}
		return m, nil
	case calDoneMsg:
		return m.finishCalibration(msg)
	case sessionStartedMsg:
		return m.sessionStarted(msg)
	case singTickMsg:
		return m.singTick(msg)
	case sessionDoneMsg:
		return m.sessionDone(msg)
	case tea.PasteMsg:
		if m.screen == screenQueue {
			return m.paste(msg.Content)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		switch m.screen {
		case screenQueue:
			return m.queueKey(msg)
		case screenCalConfirm:
			return m.calConfirmKey(msg)
		case screenCalibrating:
			if msg.String() == "esc" && m.cancelCal != nil {
				m.cancelCal()
			}
			return m, nil
		case screenSing:
			return m.singKey(msg)
		case screenResults:
			if msg.String() == "enter" || msg.String() == "esc" || msg.String() == "q" {
				m.screen = screenQueue
			}
			return m, nil
		}
	}
	// Anything else (cursor blinks, for one) belongs to the input.
	if m.screen == screenQueue && m.inputFocus {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// quit leaves the program, stopping a song that is being sung and a
// calibration that is running first.
func (m karaokeModel) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	if m.cancelCal != nil {
		m.cancelCal()
	}
	sess := m.sing.sess
	return m, func() tea.Msg {
		if sess != nil {
			_, _ = sess.Stop()
			_ = sess.Close()
		}
		return tea.QuitMsg{}
	}
}

func (m *karaokeModel) setNotice(s string, isErr bool) { m.notice, m.noticeErr = s, isErr }

func (m karaokeModel) View() tea.View {
	var v tea.View
	if m.quitting {
		v = tea.NewView("")
	} else {
		var b strings.Builder
		b.WriteString("\n  " + ui.Banner(i18n.T("karaoke.tui.subtitle")) + "\n")
		switch m.screen {
		case screenQueue:
			b.WriteString(m.queueView())
		case screenCalConfirm:
			b.WriteString(m.calConfirmView())
		case screenCalibrating:
			b.WriteString(m.calibratingView())
		case screenSing:
			b.WriteString(m.singView())
		case screenResults:
			b.WriteString(m.resultsView())
		}
		v = tea.NewView(b.String())
	}
	v.AltScreen = true
	return v
}

// headerView is the status line under the banner: calibration, device, players.
func (m karaokeModel) headerView() string {
	var cal string
	switch {
	case m.unsupported:
		cal = ui.Warn.Render(i18n.T("karaoke.doctor.unsupported"))
	case m.calErr != nil:
		cal = ui.Warn.Render(m.calErr.Error())
	case m.cal == nil:
		cal = ui.Muted.Render(i18n.T("karaoke.tui.cal_checking"))
	case !m.cal.Found:
		cal = ui.Warn.Render(i18n.T("karaoke.tui.cal_missing"))
	case m.cal.Stale:
		cal = ui.Warn.Render(i18n.Tf("karaoke.tui.cal_stale", m.cal.Reason))
	default:
		cal = ui.OK.Render(i18n.Tf("karaoke.tui.cal_ok", humanAge(m.cal.Age)))
	}

	device := i18n.T("karaoke.tui.device_default")
	switch {
	case m.cal != nil && m.cal.Key.Device != "":
		device = m.cal.Key.Device
	case m.cfg.Device != "":
		device = m.cfg.Device
	}
	players := make([]string, len(m.cfg.Inputs))
	for i, in := range m.cfg.Inputs {
		players[i] = fmt.Sprintf("%s (%d)", in.Name, in.Channel)
	}
	return "  " + cal + "\n  " +
		ui.Muted.Render(i18n.Tf("karaoke.tui.device", device)+"   "+i18n.Tf("karaoke.tui.players", strings.Join(players, ", "))) + "\n"
}

// bar draws a progress bar of width cells.
func bar(fraction float64, width int) string {
	fraction = min(max(fraction, 0), 1)
	n := int(fraction*float64(width) + 0.5)
	return ui.Accent.Render(strings.Repeat("█", n)) + ui.Muted.Render(strings.Repeat("·", width-n))
}

// kTrunc shortens s to at most n display cells, ending in an ellipsis.
func kTrunc(s string, n int) string {
	if n <= 0 || lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m karaokeModel) spinner() string { return spinnerFrames[m.spin%len(spinnerFrames)] }

// clock formats a duration as m:ss.
func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
