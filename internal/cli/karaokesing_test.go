package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/ui"
)

// laneOf is the lane a player snapshot draws, without colour.
func laneOf(p live.PlayerSnapshot) string { return plain(pitchLane(p)) }

// laneExpect builds the expected lane: dots, the three-cell target band at the
// centre, and an optional marker at col.
func laneExpect(marker int) string {
	cells := []rune(strings.Repeat("·", laneWidth))
	for c := laneCentre - 1; c <= laneCentre+1; c++ {
		cells[c] = '▒'
	}
	if marker >= 0 {
		cells[marker] = '●'
	}
	return string(cells)
}

func TestTheLaneDrawsTheTargetBandAndTheSungMarker(t *testing.T) {
	p := live.PlayerSnapshot{TargetMIDI: 60, TargetVoiced: true}
	if got, want := laneOf(p), laneExpect(-1); got != want {
		t.Errorf("target only:\n got %s\nwant %s", got, want)
	}

	for _, tc := range []struct {
		name string
		sung float64
		col  int
	}{
		{"on target", 60, laneCentre},
		{"a tone sharp", 62, laneCentre + 4},
		{"a semitone flat", 59, laneCentre - 2},
		{"six semitones sharp, the edge", 65.9, laneWidth - 1},
	} {
		p.Voiced, p.SungMIDI = true, tc.sung
		if got, want := laneOf(p), laneExpect(tc.col); got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
	}
}

func TestOctaveFoldingKeepsTheMarkerInsideTheLane(t *testing.T) {
	p := live.PlayerSnapshot{TargetMIDI: 60, TargetVoiced: true, Voiced: true}
	for _, tc := range []struct {
		sung float64
		col  int
	}{
		{72, laneCentre}, // an octave up is the same note
		{48, laneCentre}, // and an octave down
		{74, laneCentre + 4},
		{36, laneCentre},
	} {
		p.SungMIDI = tc.sung
		if got, want := laneOf(p), laneExpect(tc.col); got != want {
			t.Errorf("sung %.0f:\n got %s\nwant %s", tc.sung, got, want)
		}
	}
	// However far the pitch is, it never leaves the lane.
	for sung := 20.0; sung < 100; sung += 0.37 {
		p.SungMIDI = sung
		if w := len([]rune(laneOf(p))); w != laneWidth {
			t.Fatalf("sung %.2f draws %d cells, want %d", sung, w, laneWidth)
		}
	}
}

func TestAnUnvoicedFrameHasNoMarker(t *testing.T) {
	p := live.PlayerSnapshot{TargetMIDI: 60, TargetVoiced: true, Voiced: false, SungMIDI: 60}
	if strings.Contains(laneOf(p), "●") {
		t.Errorf("unvoiced frame drew a marker: %s", laneOf(p))
	}
}

func TestTheMarkerIsGreenOnAHitAndRedOnAMiss(t *testing.T) {
	p := live.PlayerSnapshot{TargetMIDI: 60, TargetVoiced: true, Voiced: true, SungMIDI: 60, Last: score.EventHit}
	if !strings.Contains(pitchLane(p), ui.OK.Render("●")) {
		t.Error("a hit should be drawn in the OK colour")
	}
	p.Last = score.EventMiss
	if !strings.Contains(pitchLane(p), ui.Err.Render("●")) || strings.Contains(pitchLane(p), ui.OK.Render("●")) {
		t.Error("a miss should be drawn in the error colour")
	}
}

// singingSnap is a moment in a song: 12 s into 3:20, on the first line, with
// two players.
func singingSnap() live.Snapshot {
	return live.Snapshot{
		Position: 12 * time.Second, Duration: 200 * time.Second,
		LineIndex: 0, LineProgress: 0.5,
		Players: []live.PlayerSnapshot{
			{Name: "Ana", Channel: 1, Score: 1234, Streak: 1500 * time.Millisecond, Last: score.EventHit,
				SungMIDI: 62, Voiced: true, TargetMIDI: 62, TargetVoiced: true, LevelDBFS: -18},
			{Name: "Bruno", Channel: 2, Score: 56, Last: score.EventMiss, TargetMIDI: 62, TargetVoiced: true, LevelDBFS: -80},
		},
	}
}

func placeholderLines() []lyrics.Line {
	return []lyrics.Line{
		{Start: 10 * time.Second, End: 15 * time.Second, Text: "alpha bravo charlie"},
		{Start: 15 * time.Second, End: 18 * time.Second, Text: ""},
		{Start: 18 * time.Second, End: 24 * time.Second, Text: "delta echo foxtrot"},
	}
}

// singing starts the first ready song over a scripted session and returns
// the model on the sing view.
func singing(t *testing.T, cfgMut func(*karaokeModel)) (karaokeModel, *fakeSession, *fakeWorker, *queue.Store, queue.Entry) {
	t.Helper()
	sess := &fakeSession{snap: singingSnap(), res: live.Results{Players: []live.PlayerResult{
		{Name: "Ana", Channel: 1}, {Name: "Bruno", Channel: 2}}}}
	sess.res.Players[0].Score, sess.res.Players[1].Score = 9100, 400
	host := &fakeHost{status: calibrated(), sess: sess, lines: placeholderLines()}
	m, store, w := newTUI(t, host)
	if cfgMut != nil {
		cfgMut(&m)
	}
	e := addReady(t, store, tuiURLA, "Alpha Placeholder")
	m = withStatus(m.focusList(), host)

	m, cmd := ksendCmd(m, tuiKey("enter"))
	if m.screen != screenSing {
		t.Fatalf("screen = %d, want the sing view; notice %q", m.screen, m.notice)
	}
	started, ok := msgOf(cmd).(sessionStartedMsg)
	if !ok || started.err != nil {
		t.Fatalf("starting produced %#v", started)
	}
	m = ksend(m, started)
	return m, sess, w, store, e
}

func TestTheSingViewShowsTitleProgressLyricsAndPlayers(t *testing.T) {
	m, _, _, _, _ := singing(t, nil)
	out := kscreen(m)
	for _, want := range []string{
		"Alpha Placeholder", "0:12", "3:20",
		"alpha bravo charlie", "delta echo foxtrot", // the current line, then the next non-blank one
		"Ana", "1234", "streak 1.5s", "Bruno", "56",
		laneExpect(laneCentre),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sing view lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "●") != 1 {
		t.Errorf("only the voiced player should have a marker:\n%s", out)
	}
}

func TestTheLyricHighlightFollowsTheLineProgress(t *testing.T) {
	m, _, _, _, _ := singing(t, nil)
	cur, _ := m.lyricLines(m.sing.snap)
	// "alpha bravo charlie" is 19 runes; half of it, rounded, is 10.
	want := ui.Accent.Bold(true).Render("alpha brav") + ui.Heading.Render("o charlie")
	if cur != want {
		t.Errorf("highlight = %q, want %q", cur, want)
	}
}

func TestBeforeTheFirstLineTheNextOneIsShown(t *testing.T) {
	m, sess, _, _, _ := singing(t, nil)
	sess.snap.LineIndex, sess.snap.Position = -1, 2*time.Second
	m.sing.snap = sess.Snapshot()
	cur, next := m.lyricLines(m.sing.snap)
	if cur != "" || next != "alpha bravo charlie" {
		t.Errorf("cur=%q next=%q", cur, next)
	}
}

func TestSessionWarningsShowAsOneLine(t *testing.T) {
	m, sess, _, _, _ := singing(t, nil)
	sess.snap.Warnings = []string{"no echo path on input 2", "audio was dropped (512 samples)"}
	m = ksend(m, singTickMsg{})
	out := kscreen(m)
	if !strings.Contains(out, "no echo path on input 2 · audio was dropped (512 samples)") {
		t.Errorf("warnings not on one line:\n%s", out)
	}
}

func TestSpacePausesAndResumesTheSession(t *testing.T) {
	m, sess, _, _, _ := singing(t, nil)
	m = kpress(m, "space")
	if !sess.paused || !strings.Contains(kscreen(m), "PAUSED") {
		t.Fatalf("paused=%v:\n%s", sess.paused, kscreen(m))
	}
	m = kpress(m, "space")
	if sess.paused || strings.Contains(kscreen(m), "PAUSED") {
		t.Errorf("space again should resume; paused=%v", sess.paused)
	}
}

func TestEscAsksThenStopsAndShowsTheResults(t *testing.T) {
	m, sess, w, store, e := singing(t, nil)

	m = kpress(m, "esc")
	if !strings.Contains(kscreen(m), "Stop this song early?") || !sess.paused {
		t.Fatalf("no question, or the song kept playing under it:\n%s", kscreen(m))
	}
	m = kpress(m, "n")
	if sess.paused || strings.Contains(kscreen(m), "Stop this song early?") {
		t.Fatal("no should carry on singing")
	}

	m = kpress(m, "esc")
	m, cmd := ksendCmd(m, tuiKey("y"))
	done, ok := msgOf(cmd).(sessionDoneMsg)
	if !ok || !sess.stopped {
		t.Fatalf("stopping produced %#v (stopped=%v)", done, sess.stopped)
	}
	m = ksend(m, done)

	if m.screen != screenResults || !sess.closed {
		t.Errorf("screen=%d closed=%v, want results", m.screen, sess.closed)
	}
	if w.paused || w.resumes != 1 {
		t.Errorf("worker paused=%v resumes=%d, want it resumed once", w.paused, w.resumes)
	}
	got, _ := store.Get(e.ID)
	if got.State != queue.Sung || len(got.Scores) != 1 || got.Scores[0].Completed {
		t.Errorf("entry = %+v, want sung with one incomplete score", got)
	}
}

func TestTheSongEndingNaturallyGoesToTheResults(t *testing.T) {
	m, sess, _, store, e := singing(t, nil)
	sess.snap.Done = true
	m, cmd := ksendCmd(m, singTickMsg{})
	done, ok := msgOf(cmd).(sessionDoneMsg)
	if !ok || sess.stopped {
		t.Fatalf("the end produced %#v (stopped=%v)", done, sess.stopped)
	}
	m = ksend(m, done)
	if m.screen != screenResults {
		t.Fatalf("screen = %d", m.screen)
	}
	got, _ := store.Get(e.ID)
	if got.State != queue.Sung || !got.Scores[0].Completed || got.Scores[0].Players[0].Score != 9100 {
		t.Errorf("entry = %+v", got)
	}
}

func TestTheWorkerIsPausedWhileSingingAndResumedAfterwards(t *testing.T) {
	m, sess, w, _, _ := singing(t, nil)
	if !w.paused || w.pauses != 1 {
		t.Fatalf("worker paused=%v pauses=%d on entering the sing view", w.paused, w.pauses)
	}
	sess.snap.Done = true
	m, cmd := ksendCmd(m, singTickMsg{})
	m = ksend(m, msgOf(cmd))
	if w.paused || w.resumes != 1 {
		t.Errorf("worker paused=%v resumes=%d on leaving", w.paused, w.resumes)
	}
	_ = m
}

func TestTheWorkerKeepsRunningWhenTheSettingSaysSo(t *testing.T) {
	_, _, w, _, _ := singing(t, func(m *karaokeModel) { m.cfg.PausePrepWhileSinging = false })
	if w.pauses != 0 {
		t.Errorf("worker was paused %d times with pause_prep_while_singing off", w.pauses)
	}
}

func TestASessionThatCannotStartReturnsToTheQueueAndResumesTheWorker(t *testing.T) {
	host := &fakeHost{status: calibrated(), startErr: errNotCalibrated}
	m, store, w := newTUI(t, host)
	addReady(t, store, tuiURLA, "Alpha Placeholder")
	m = withStatus(m.focusList(), host)

	m, cmd := ksendCmd(m, tuiKey("enter"))
	m = ksend(m, msgOf(cmd))
	if m.screen != screenCalConfirm || w.paused {
		t.Errorf("screen=%d paused=%v, want the calibration question and a running worker", m.screen, w.paused)
	}

	host.startErr = errKTUI
	m = ksend(m, tuiKey("esc"))
	m, cmd = ksendCmd(m, tuiKey("enter"))
	m = ksend(m, msgOf(cmd))
	if m.screen != screenQueue || !strings.Contains(m.notice, "could not start the song") || w.paused {
		t.Errorf("screen=%d notice=%q paused=%v", m.screen, m.notice, w.paused)
	}
}

func TestCtrlCDuringASongStopsItBeforeQuitting(t *testing.T) {
	m, sess, _, _, _ := singing(t, nil)
	m, cmd := ksendCmd(m, tuiKey("ctrl+c"))
	if msg := msgOf(cmd); !m.quitting || !sess.stopped || !sess.closed || msg == nil {
		t.Errorf("quitting=%v stopped=%v closed=%v msg=%v", m.quitting, sess.stopped, sess.closed, msg)
	}
}

var errKTUI = errors.New("scripted failure")
