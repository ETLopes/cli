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
// results view.
func atResults(t *testing.T, res live.Results) (karaokeModel, *queue.Store, queue.Entry) {
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
		"Results: Alpha Placeholder", "9100", "400", "87%", "24 cents", "sharp", "flat", "4.2s",
		// Lines are shown by when they start, never by their words.
		"0:10", "0:18",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results lack %q:\n%s", want, out)
		}
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
	m = ksend(m, msgOf(cmd))
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
	if out := kscreen(m); !strings.Contains(out, "★") || !strings.Contains(out, "Ana 9100") {
		t.Errorf("the sung row should show the score:\n%s", out)
	}
}
