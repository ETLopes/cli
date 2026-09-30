package cli

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

func TestTypingAURLAndEnterAddsARow(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	m = kpress(ktype(m, tuiURLA), "enter")

	es := store.Entries()
	if len(es) != 1 || es[0].URL != tuiURLA || es[0].State != queue.Queued {
		t.Fatalf("queue = %+v, want one queued entry", es)
	}
	if out := kscreen(m); !strings.Contains(out, tuiURLA[:30]) || !strings.Contains(out, "queued 1 song(s)") {
		t.Errorf("screen lacks the new row or the notice:\n%s", out)
	}
	if m.input.Value() != "" {
		t.Errorf("input still holds %q after adding", m.input.Value())
	}
}

func TestAnInvalidURLShowsAnErrorAndNoRow(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	m = kpress(ktype(m, "not a url"), "enter")

	if n := len(store.Entries()); n != 0 {
		t.Fatalf("queue has %d entries, want 0", n)
	}
	if out := kscreen(m); !strings.Contains(out, "URL must start with http") {
		t.Errorf("no inline error:\n%s", out)
	}
}

func TestADuplicateShowsTheNoticeAndNoSecondRow(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	m = kpress(ktype(m, tuiURLA), "enter")
	m = kpress(ktype(m, tuiURLA), "enter")

	if n := len(store.Entries()); n != 1 {
		t.Fatalf("queue has %d entries, want 1", n)
	}
	if out := kscreen(m); !strings.Contains(out, "already in the queue") {
		t.Errorf("no duplicate notice:\n%s", out)
	}
}

func TestPastingSeveralURLsAddsThemAll(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	m = ksend(m, tea.PasteMsg{Content: tuiURLA + "\n" + tuiURLB + "\nnot-a-url\n" + tuiURLC})

	if n := len(store.Entries()); n != 3 {
		t.Fatalf("queue has %d entries, want 3", n)
	}
	out := kscreen(m)
	if !strings.Contains(out, "queued 3 song(s)") || !strings.Contains(out, "not-a-url") {
		t.Errorf("notice should count the additions and name the bad word:\n%s", out)
	}
}

func TestPastingOneURLLandsInTheInputToEditFirst(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	m = ksend(m, tea.PasteMsg{Content: tuiURLA})
	if len(store.Entries()) != 0 || m.input.Value() != tuiURLA {
		t.Errorf("entries=%d input=%q, want the URL in the input only", len(store.Entries()), m.input.Value())
	}
}

func TestWorkerEventsMoveTheRowsStageBarAndGlyph(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	e, err := store.Add(tuiURLA)
	fxMust(t, err)
	store.ClaimNext()
	m = m.focusList()

	m = ksend(m, workerEventMsg{queue.Event{ID: e.ID, Kind: queue.EventProgress, Stage: song.StageSeparate, Fraction: 0.5}})
	out := kscreen(m)
	for _, want := range []string{"Isolating the vocals", "50%", "█████·····", "⠋"} {
		if !strings.Contains(out, want) {
			t.Errorf("preparing row lacks %q:\n%s", want, out)
		}
	}

	fxMust(t, store.Finish(e.ID, "/songs/x", "Synthetic Placeholder"))
	m = ksend(m, workerEventMsg{queue.Event{ID: e.ID, Kind: queue.EventState, State: queue.Ready}})
	out = kscreen(m)
	if !strings.Contains(out, "●") || !strings.Contains(out, "Synthetic Placeholder") || strings.Contains(out, "Isolating") {
		t.Errorf("ready row should show the title and glyph, not the stage:\n%s", out)
	}
}

func TestFailedAndSungRowsShowTheirOutcome(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	bad, err := store.Add(tuiURLA)
	fxMust(t, err)
	store.ClaimNext()
	fxMust(t, store.Fail(bad.ID, errors.New("download blew up")))
	sung := addReady(t, store, tuiURLB, "Sung Placeholder")
	fxMust(t, store.RecordScore(sung.ID, queue.Score{Players: []queue.Player{{Name: "Ana", Score: 8123}}}))
	fxMust(t, store.SetState(sung.ID, queue.Sung))

	out := kscreen(m)
	for _, want := range []string{"✗", "download blew up", "★", "Ana 8123"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q:\n%s", want, out)
		}
	}
}

func TestAnEventForARemovedEntryIsIgnored(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	e, err := store.Add(tuiURLA)
	fxMust(t, err)
	fxMust(t, store.Remove(e.ID))

	for _, ev := range []queue.Event{
		{ID: e.ID, Kind: queue.EventProgress, Stage: song.StageDownload, Fraction: 0.3},
		{ID: e.ID, Kind: queue.EventState, State: queue.Failed, Err: "cancelled"},
		{ID: e.ID, Kind: queue.EventWarning, Warning: "no lyrics"},
	} {
		m = ksend(m, workerEventMsg{ev})
	}
	if len(m.progress) != 0 || m.notice != "" {
		t.Errorf("stale events left progress=%v notice=%q", m.progress, m.notice)
	}
}

func TestReorderingAndRemovalGoThroughTheStore(t *testing.T) {
	m, store, w := newTUI(t, &fakeHost{})
	a := addReady(t, store, tuiURLA, "Alpha Placeholder")
	b := addReady(t, store, tuiURLB, "Bravo Placeholder")
	_ = a
	m = m.focusList()

	m = kpress(m, "down", "shift+up")
	ids := func() []string {
		var out []string
		for _, e := range store.Entries() {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(); got[0] != b.ID || m.cursor != 0 {
		t.Fatalf("after shift+up the order is %v with the cursor at %d, want %s first and the cursor following", got, m.cursor, b.ID)
	}
	m = kpress(m, "J")
	if got := ids(); got[1] != b.ID || m.cursor != 1 {
		t.Fatalf("after J the order is %v (cursor %d), want %s last", got, m.cursor, b.ID)
	}

	m = kpress(m, "d")
	if len(store.Entries()) != 1 || len(w.removed) != 1 || w.removed[0] != b.ID {
		t.Errorf("entries=%d removed=%v, want %s removed", len(store.Entries()), w.removed, b.ID)
	}
	if !strings.Contains(kscreen(m), "removed Bravo Placeholder") {
		t.Errorf("no removal notice:\n%s", kscreen(m))
	}
}

func TestRemovingAnEntryThatIsPreparingAsksFirst(t *testing.T) {
	m, store, w := newTUI(t, &fakeHost{})
	e, err := store.Add(tuiURLA)
	fxMust(t, err)
	store.ClaimNext()
	m = m.focusList()

	m = kpress(m, "d")
	if len(w.removed) != 0 || !strings.Contains(kscreen(m), "Remove it anyway?") {
		t.Fatalf("removed=%v; want a question, not a removal:\n%s", w.removed, kscreen(m))
	}
	m = kpress(m, "n")
	if len(w.removed) != 0 || len(store.Entries()) != 1 {
		t.Fatal("answering no still removed the entry")
	}
	m = kpress(m, "delete", "y")
	if len(w.removed) != 1 || w.removed[0] != e.ID {
		t.Errorf("removed=%v after confirming, want [%s]", w.removed, e.ID)
	}
}

func TestRRequeuesAFailedEntry(t *testing.T) {
	m, store, _ := newTUI(t, &fakeHost{})
	e, err := store.Add(tuiURLA)
	fxMust(t, err)
	store.ClaimNext()
	fxMust(t, store.Fail(e.ID, errors.New("boom")))
	m = m.focusList()

	m = kpress(m, "r")
	if got, _ := store.Get(e.ID); got.State != queue.Queued {
		t.Errorf("state = %s, want queued", got.State)
	}
	// Only failed entries can be retried.
	m = kpress(m, "r")
	if !strings.Contains(kscreen(m), "Only a failed song can be retried") {
		t.Errorf("no hint for a retry of a non-failed entry:\n%s", kscreen(m))
	}
}

func TestEnterWithNothingReadySaysSo(t *testing.T) {
	host := &fakeHost{status: calibrated()}
	m, store, _ := newTUI(t, host)
	_, err := store.Add(tuiURLA)
	fxMust(t, err)
	m = withStatus(m.focusList(), host)

	m = kpress(m, "enter")
	if m.screen != screenQueue || !strings.Contains(kscreen(m), "Nothing ready yet") {
		t.Errorf("screen=%d:\n%s", m.screen, kscreen(m))
	}
}

func TestEnterWhenNotCalibratedRoutesToCalibrationWithAnExplanation(t *testing.T) {
	host := &fakeHost{}
	m, store, _ := newTUI(t, host)
	addReady(t, store, tuiURLA, "Alpha Placeholder")
	m = withStatus(m.focusList(), host)

	m = kpress(m, "enter")
	if m.screen != screenCalConfirm {
		t.Fatalf("screen = %d, want the calibration question", m.screen)
	}
	out := kscreen(m)
	for _, want := range []string{"You need to calibrate before singing", "A sweep and noise will play through the speakers", "Calibrate now?"} {
		if !strings.Contains(out, want) {
			t.Errorf("calibration question lacks %q:\n%s", want, out)
		}
	}
	m = kpress(m, "esc")
	if m.screen != screenQueue {
		t.Error("esc should return to the queue")
	}
}

func TestConfirmingCalibrationRunsItAndRefreshesTheStatus(t *testing.T) {
	host := &fakeHost{}
	m, _, w := newTUI(t, host)
	m = withStatus(m, host)
	m = kpress(m.focusList(), "c")
	if m.screen != screenCalConfirm {
		t.Fatalf("screen = %d", m.screen)
	}
	m, cmd := ksendCmd(m, tuiKey("enter"))
	if m.screen != screenCalibrating || !w.paused {
		t.Fatalf("screen=%d paused=%v, want calibrating with the worker paused", m.screen, w.paused)
	}
	done, ok := msgOf(cmd).(calDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("calibration produced %#v", done)
	}
	m, cmd = ksendCmd(m, done)
	if m.screen != screenQueue || w.paused || !strings.Contains(kscreen(m), "Calibration saved") {
		t.Errorf("screen=%d paused=%v:\n%s", m.screen, w.paused, kscreen(m))
	}
	if _, ok := msgOf(cmd).(calStatusMsg); !ok {
		t.Error("finishing should reload the calibration status")
	}
}

func TestAStubBuildSaysLiveAudioIsMacOnlyButKeepsTheQueue(t *testing.T) {
	host := &fakeHost{statusErr: audioio.ErrUnsupported}
	m, store, _ := newTUI(t, host)
	addReady(t, store, tuiURLA, "Alpha Placeholder")
	m = withStatus(m.focusList(), host)

	if !strings.Contains(kscreen(m), "macOS-only") {
		t.Errorf("header lacks the notice:\n%s", kscreen(m))
	}
	m = kpress(m, "enter")
	if m.screen != screenQueue || !strings.Contains(m.notice, "macOS-only") {
		t.Errorf("singing: screen=%d notice=%q", m.screen, m.notice)
	}
	m = kpress(m, "c")
	if m.screen != screenQueue {
		t.Error("calibrating on a stub build should not open the calibration flow")
	}
	// The queue itself still works.
	m = kpress(m, "tab")
	m = kpress(ktype(m, tuiURLB), "enter")
	if n := len(store.Entries()); n != 2 {
		t.Errorf("queue has %d entries, want 2", n)
	}
}

func TestExportShowsWhereTheSongWent(t *testing.T) {
	host := &fakeHost{status: calibrated()}
	m, store, _ := newTUI(t, host)
	e := addReady(t, store, tuiURLA, "Alpha Placeholder")
	m = m.focusList()

	m, cmd := ksendCmd(m, tuiKey("e"))
	msg := msgOf(cmd)
	if _, ok := msg.(exportDoneMsg); !ok {
		t.Fatalf("export produced %#v", msg)
	}
	m = ksend(m, msg)
	if len(host.exports) != 1 || host.exports[0] != e.ID || !strings.Contains(kscreen(m), "Exported to /out/"+e.ID) {
		t.Errorf("exports=%v:\n%s", host.exports, kscreen(m))
	}
}

func TestTheHeaderShowsCalibrationDeviceAndPlayers(t *testing.T) {
	host := &fakeHost{status: calibrated()}
	m, _, _ := newTUI(t, host)
	if !strings.Contains(kscreen(m), "Checking the calibration") {
		t.Errorf("before the status arrives:\n%s", kscreen(m))
	}
	m = withStatus(m, host)
	out := kscreen(m)
	for _, want := range []string{"Calibration ok", "Fake Interface", "Ana (1)", "Bruno (2)"} {
		if !strings.Contains(out, want) {
			t.Errorf("header lacks %q:\n%s", want, out)
		}
	}

	stale := calibrated()
	stale.Stale, stale.Reason = true, "the calibration is 40 days old"
	m = ksend(m, calStatusMsg{status: stale})
	if out := kscreen(m); !strings.Contains(out, "stale") || !strings.Contains(out, "press c") {
		t.Errorf("stale header:\n%s", out)
	}
	m = ksend(m, calStatusMsg{status: CalibrationStatus{}})
	if out := kscreen(m); !strings.Contains(out, "Not calibrated") {
		t.Errorf("missing header:\n%s", out)
	}
}

func TestACorruptQueueBannerIsShown(t *testing.T) {
	m, _, _ := newTUI(t, &fakeHost{})
	m.banner = "The saved queue could not be read and was moved to /x/queue.bad"
	if !strings.Contains(kscreen(m), "moved to /x/queue.bad") {
		t.Errorf("no banner:\n%s", kscreen(m))
	}
}

func TestQuitStopsTheProgram(t *testing.T) {
	m, _, _ := newTUI(t, &fakeHost{})
	m, cmd := ksendCmd(m.focusList(), tuiKey("q"))
	if _, ok := msgOf(cmd).(tea.QuitMsg); !ok || !m.quitting {
		t.Error("q should quit")
	}
}
