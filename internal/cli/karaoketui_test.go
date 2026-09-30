package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/ultrastar"
)

// The TUI tests drive the model with messages and read the screen back, as the
// meter tests do. The collaborators are scripted; the store is the real one.

const (
	tuiURLA = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	tuiURLB = "https://www.youtube.com/watch?v=bbbbbbbbbbb"
	tuiURLC = "https://www.youtube.com/watch?v=ccccccccccc"
)

// tuiKey builds the key press a terminal would send for name.
func tuiKey(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	case "delete":
		return tea.KeyPressMsg{Code: tea.KeyDelete}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(name)
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// fakeHost is the application as the screen sees it.
type fakeHost struct {
	status    CalibrationStatus
	statusErr error
	sess      *fakeSession
	startErr  error
	lines     []lyrics.Line
	exports   []string
	exportErr error
}

func (h *fakeHost) Calibration() (CalibrationStatus, error) { return h.status, h.statusErr }
func (h *fakeHost) Calibrate(context.Context, func(calibrate.Stage, float64)) (calibrate.Result, error) {
	return calibrate.Result{}, nil
}
func (h *fakeHost) SaveCalibration(calibrate.Result) error { return nil }
func (h *fakeHost) StartSession(context.Context, queue.Entry, []config.KaraokeInput) (karaokeSession, error) {
	if h.startErr != nil {
		return nil, h.startErr
	}
	return h.sess, nil
}
func (h *fakeHost) Lyrics(queue.Entry) []lyrics.Line { return h.lines }
func (h *fakeHost) Export(_ context.Context, e queue.Entry, _ string) (ultrastar.Result, error) {
	h.exports = append(h.exports, e.ID)
	return ultrastar.Result{Dir: "/out/" + e.ID}, h.exportErr
}

// fakeSession plays back scripted snapshots.
type fakeSession struct {
	snap    live.Snapshot
	res     live.Results
	paused  bool
	stopped bool
	closed  bool
}

func (s *fakeSession) Snapshot() live.Snapshot {
	snap := s.snap
	snap.Paused = s.paused
	return snap
}
func (s *fakeSession) Pause()  { s.paused = true }
func (s *fakeSession) Resume() { s.paused = false }
func (s *fakeSession) Stop() (live.Results, error) {
	s.stopped = true
	r := s.res
	r.Incomplete = true
	return r, nil
}
func (s *fakeSession) Wait() (live.Results, error) { return s.res, nil }
func (s *fakeSession) Close() error                { s.closed = true; return nil }

// fakeWorker records what the screen asks of the background worker; removal
// goes to the store, as the real worker's does.
type fakeWorker struct {
	store   *queue.Store
	paused  bool
	pauses  int
	resumes int
	removed []string
}

func (w *fakeWorker) Pause()         { w.paused = true; w.pauses++ }
func (w *fakeWorker) Resume()        { w.paused = false; w.resumes++ }
func (w *fakeWorker) CancelCurrent() {}
func (w *fakeWorker) Remove(id string) error {
	w.removed = append(w.removed, id)
	return w.store.Remove(id)
}

func tuiConfig() config.Karaoke {
	return config.Karaoke{
		Difficulty:            "medium",
		Inputs:                []config.KaraokeInput{{Channel: 1, Name: "Ana"}, {Channel: 2, Name: "Bruno"}},
		PausePrepWhileSinging: true,
	}
}

// calibrated is a status that lets a song be sung.
func calibrated() CalibrationStatus {
	return CalibrationStatus{Found: true, Key: calibrate.Key{Device: "Fake Interface", SampleRate: 48000}, Age: 3 * time.Hour}
}

// newTUI builds a model over a real store in a temp dir, with the calibration
// status already loaded.
func newTUI(t *testing.T, host *fakeHost) (karaokeModel, *queue.Store, *fakeWorker) {
	t.Helper()
	store, _, err := queue.Open(filepath.Join(t.TempDir(), "queue.json"))
	fxMust(t, err)
	w := &fakeWorker{store: store}
	m := newKaraokeModel(context.Background(), host, store, w, nil, tuiConfig())
	return m, store, w
}

func ksend(m karaokeModel, msg tea.Msg) karaokeModel {
	m, _ = ksendCmd(m, msg)
	return m
}

func ksendCmd(m karaokeModel, msg tea.Msg) (karaokeModel, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(karaokeModel), cmd
}

// press sends each named key in turn.
func kpress(m karaokeModel, names ...string) karaokeModel {
	for _, n := range names {
		m = ksend(m, tuiKey(n))
	}
	return m
}

// typeText types s one character at a time.
func ktype(m karaokeModel, s string) karaokeModel {
	for _, r := range s {
		m = ksend(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// msgOf runs a command and returns what it produced, or nil when it is still
// waiting after a moment (an event wait, say).
func msgOf(cmd tea.Cmd) tea.Msg { return msgWithin(cmd, 300*time.Millisecond) }

// msgWithin is msgOf with a patience of d, for commands that do real work.
func msgWithin(cmd tea.Cmd, d time.Duration) tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(d):
		return nil
	}
}

// screen is what the model draws, without colour.
func kscreen(m karaokeModel) string { return plain(m.View().Content) }

func withStatus(m karaokeModel, host *fakeHost) karaokeModel {
	st, err := host.Calibration()
	return ksend(m, calStatusMsg{status: st, err: err})
}

func addReady(t *testing.T, store *queue.Store, url, title string) queue.Entry {
	t.Helper()
	e, err := store.Add(url)
	fxMust(t, err)
	_, ok := store.ClaimNext()
	if !ok {
		t.Fatal("nothing to claim")
	}
	fxMust(t, store.Finish(e.ID, filepath.Join(t.TempDir(), "song"), title))
	got, _ := store.Get(e.ID)
	return got
}
