package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/score"
)

func resultsFor(players ...live.PlayerResult) live.Results { return live.Results{Players: players} }

func player(name string, points int, tendency score.Tendency) live.PlayerResult {
	p := live.PlayerResult{Name: name}
	p.Score, p.InTunePercent, p.MeanAbsCents, p.Tendency = points, 87.4, 23.6, tendency
	p.LongestStreak = 4200 * time.Millisecond
	p.BestLine, p.WorstLine = 0, 2
	return p
}

// atResults finishes a scripted session with res and returns the model on the
// results view, the reveal skipped.
func atResults(t *testing.T, res live.Results) (karaokeModel, *queue.Store, queue.Entry) {
	t.Helper()
	m, store, e := atReveal(t, res)
	m = kpress(m, "enter")
	if !m.results.reveal.done() {
		t.Fatal("enter did not skip the reveal")
	}
	return m, store, e
}

// atReveal finishes a scripted session with res and returns the model at the
// start of the reveal.
func atReveal(t *testing.T, res live.Results) (karaokeModel, *queue.Store, queue.Entry) {
	t.Helper()
	m, sess, _, store, e := singing(t, nil)
	sess.res = res
	sess.snap.Done = true
	m, cmd := ksendCmd(m, singTickMsg{})
	m = ksend(m, msgOf(cmd))
	if m.screen != screenResults {
		t.Fatalf("screen = %d, want results", m.screen)
	}
	return m, store, e
}

func TestResultsShowEveryMetricPerPlayer(t *testing.T) {
	m, _, _ := atResults(t, resultsFor(player("Ana", 9100, score.TendencySharp), player("Bruno", 400, score.TendencyFlat)))
	out := kscreen(m)
	for _, want := range []string{
		"Results: Alpha Placeholder", "87%", "24 cents", "sharp", "flat", "4.2s",
		// Lines are shown by when they start, never by their words.
		"0:10", "0:18",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results lack %q:\n%s", want, out)
		}
	}
	if row := lineWith(out, "Score"); !strings.Contains(row, " 91 ") || !strings.HasSuffix(strings.TrimSpace(row), " 4") {
		t.Errorf("score row %q, want 91 and 4 out of 100", row)
	}
	if strings.Contains(out, "alpha bravo") || strings.Contains(out, "delta echo") {
		t.Errorf("results show lyric text:\n%s", out)
	}
}

func TestTheWinnerIsHighlighted(t *testing.T) {
	m, _, _ := atResults(t, resultsFor(player("Ana", 9100, score.TendencyCentered), player("Bruno", 400, score.TendencyFlat)))
	out := kscreen(m)
	if !strings.Contains(out, "Ana ★") || strings.Contains(out, "Bruno ★") || !strings.Contains(out, "Winner: Ana") {
		t.Errorf("winner not marked:\n%s", out)
	}

	m, _, _ = atResults(t, resultsFor(player("Ana", 500, score.TendencyCentered), player("Bruno", 500, score.TendencyFlat)))
	if out := kscreen(m); !strings.Contains(out, "Ana ★") || !strings.Contains(out, "Bruno ★") {
		t.Errorf("a tie should mark both:\n%s", out)
	}

	m, _, _ = atResults(t, resultsFor(player("Ana", 9100, score.TendencyCentered)))
	if out := kscreen(m); strings.Contains(out, "★") || strings.Contains(out, "Winner") {
		t.Errorf("a solo singer has nobody to beat:\n%s", out)
	}
}

func TestAnIncompleteSessionIsMarked(t *testing.T) {
	res := resultsFor(player("Ana", 3000, score.TendencyCentered))
	res.Incomplete = true
	m, _, _ := atResults(t, res)
	if !strings.Contains(kscreen(m), "Stopped early") {
		t.Errorf("no incomplete mark:\n%s", kscreen(m))
	}

	m, _, _ = atResults(t, resultsFor(player("Ana", 3000, score.TendencyCentered)))
	if strings.Contains(kscreen(m), "Stopped early") {
		t.Error("a finished song was marked incomplete")
	}
}

func TestAPlayerWhoNeverSangHasNoTendencyOrLines(t *testing.T) {
	p := player("Bruno", 0, score.TendencyUnknown)
	p.BestLine, p.WorstLine = -1, -1
	m, _, _ := atResults(t, resultsFor(p))
	out := kscreen(m)
	if !strings.Contains(out, "not enough singing") || !strings.Contains(out, "–") {
		t.Errorf("silent player:\n%s", out)
	}
}

func TestResultsAreRecordedOnTheQueueEntry(t *testing.T) {
	res := resultsFor(player("Ana", 9100, score.TendencyCentered), player("Bruno", 400, score.TendencyFlat))
	_, store, e := atResults(t, res)

	got, _ := store.Get(e.ID)
	if got.State != queue.Sung || len(got.Scores) != 1 {
		t.Fatalf("entry = %+v, want sung with one score", got)
	}
	sc := got.Scores[0]
	if !sc.Completed || sc.Difficulty != "medium" || len(sc.Players) != 2 ||
		sc.Players[0] != (queue.Player{Name: "Ana", Score: 9100}) || sc.Players[1].Score != 400 {
		t.Errorf("score = %+v", sc)
	}
}

func TestResultsForAnEntryRemovedMidSongStillShowWithAWarning(t *testing.T) {
	m, sess, _, store, e := singing(t, nil)
	fxMust(t, store.Remove(e.ID))
	sess.snap.Done = true
	m, cmd := ksendCmd(m, singTickMsg{})
	m = kpress(ksend(m, msgOf(cmd)), "enter") // skip the reveal
	if m.screen != screenResults || !strings.Contains(kscreen(m), "could not save the score") {
		t.Errorf("screen=%d:\n%s", m.screen, kscreen(m))
	}
}

func TestEnterOnTheResultsReturnsToTheQueueWithTheScore(t *testing.T) {
	m, _, _ := atResults(t, resultsFor(player("Ana", 9100, score.TendencyCentered)))
	m = kpress(m, "enter")
	if m.screen != screenQueue {
		t.Fatalf("screen = %d, want the queue", m.screen)
	}
	if out := kscreen(m); !strings.Contains(out, "★") || !strings.Contains(out, "Ana 91") {
		t.Errorf("the sung row should show the score:\n%s", out)
	}
}

// lineWith is the first line of out containing s.
func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

// revealAt moves the reveal's clock to d into its current turn and ticks.
func revealAt(m karaokeModel, d time.Duration) karaokeModel {
	start := m.results.reveal.start
	m.now = func() time.Time { return start.Add(d) }
	return ksend(m, revealTickMsg{})
}

func TestScoresAreShownOutOfAHundred(t *testing.T) {
	for _, tc := range []struct{ points, want int }{
		{0, 0}, {50, 0}, {400, 4}, {9100, 91}, {9999, 99}, {10000, 100}, {-5, 0}, {12000, 100},
	} {
		if got := points100(tc.points); got != tc.want {
			t.Errorf("points100(%d) = %d, want %d", tc.points, got, tc.want)
		}
	}
}

func TestTheCountUpStartsAtZeroCrawlsNearTheEndAndLandsOnTheScore(t *testing.T) {
	if got := countUp(87, 0); got != 0 {
		t.Errorf("at the start: %d", got)
	}
	if got := countUp(87, revealCount); got != 87 {
		t.Errorf("at the end: %d", got)
	}
	half := countUp(87, revealCount/2)
	if half < 87*3/4 {
		t.Errorf("halfway the count is at %d, want most of the way there", half)
	}
	prev := 0
	for d := time.Duration(0); d <= revealCount; d += revealFrame {
		n := countUp(87, d)
		if n < prev || n > 87 {
			t.Fatalf("count went %d → %d at %s", prev, n, d)
		}
		prev = n
	}
}

func TestEveryScoreGetsAVerdict(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range []int{0, 24, 25, 49, 50, 74, 75, 89, 90, 99, 100} {
		text, _ := verdict(p)
		if text == "" {
			t.Errorf("no verdict for %d", p)
		}
		seen[text] = true
	}
	if len(seen) != 6 {
		t.Errorf("%d distinct verdicts, want 6", len(seen))
	}
}

func TestTheRevealGoesFromTheLowestScoreToTheHighest(t *testing.T) {
	m, _, _ := atReveal(t, resultsFor(player("Ana", 9100, score.TendencyCentered), player("Bruno", 400, score.TendencyFlat)))

	// Bruno's turn first: suspense, with no number and nobody else's score.
	if got := m.revealNow(); got.player != 1 || got.phase != phaseSuspense {
		t.Fatalf("first frame = %+v, want Bruno's suspense", got)
	}
	out := kscreen(m)
	if !strings.Contains(out, "And Bruno scored") || strings.Contains(out, "Winner") || strings.Contains(out, "Score") {
		t.Errorf("suspense frame:\n%s", out)
	}

	m = revealAt(m, revealIntro+revealCount/2)
	if got := m.revealNow(); got.phase != phaseCounting || got.number > 4 {
		t.Errorf("counting frame = %+v", got)
	}

	m = revealAt(m, revealIntro+revealCount)
	if got := m.revealNow(); got != (revealShown{player: 1, phase: phaseLanded, number: 4}) {
		t.Errorf("landed frame = %+v", got)
	}
	if out := kscreen(m); !strings.Contains(out, "The fun is what counts!") {
		t.Errorf("no verdict once landed:\n%s", out)
	}

	// Then Ana's, the best, with Bruno's score kept above.
	m = revealAt(m, revealPerTurn)
	if got := m.revealNow(); got.player != 0 || got.phase != phaseSuspense {
		t.Fatalf("second turn = %+v, want Ana's suspense", got)
	}
	if out := kscreen(m); !strings.Contains(out, "Bruno  4") || !strings.Contains(out, "And Ana scored") {
		t.Errorf("second turn:\n%s", out)
	}

	m = revealAt(m, revealPerTurn)
	if !m.results.reveal.done() {
		t.Fatal("the reveal did not end after the last turn")
	}
	if out := kscreen(m); !strings.Contains(out, "Winner: Ana") || !strings.Contains(out, "Outstanding!") {
		t.Errorf("after the reveal:\n%s", out)
	}
}

func TestTheRevealStopsTickingOnceItIsOver(t *testing.T) {
	m, _, _ := atReveal(t, resultsFor(player("Ana", 9100, score.TendencyCentered)))
	start := m.results.reveal.start
	m.now = func() time.Time { return start.Add(revealPerTurn) }
	m, cmd := ksendCmd(m, revealTickMsg{})
	if !m.results.reveal.done() || cmd != nil {
		t.Errorf("done=%v cmd=%v, want done and no more ticks", m.results.reveal.done(), cmd != nil)
	}
}

func TestTheFirstKeySkipsTheRevealAndTheSecondLeaves(t *testing.T) {
	m, _, _ := atReveal(t, resultsFor(player("Ana", 9100, score.TendencyCentered)))
	m = kpress(m, "enter")
	if m.screen != screenResults || !m.results.reveal.done() {
		t.Fatalf("screen=%d done=%v, want the full results", m.screen, m.results.reveal.done())
	}
	if out := kscreen(m); !strings.Contains(out, "Outstanding!") {
		t.Errorf("skipped reveal lacks the verdict:\n%s", out)
	}
	m = kpress(m, "enter")
	if m.screen != screenQueue {
		t.Errorf("screen = %d, want the queue", m.screen)
	}
}
