package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/ui"
)

// resultsColumn is the width of one player's column in the results table.
const resultsColumn = 16

type resultsState struct {
	entry   queue.Entry
	res     live.Results
	lines   []lyrics.Line
	saveErr string
}

// recordScore stores the session on the queue entry and marks it sung. The
// entry may have been removed while it was being sung; that is reported, not
// fatal, since the results are still worth showing.
func (m karaokeModel) recordScore(entry queue.Entry, res live.Results) string {
	sc := queue.Score{At: m.now(), Difficulty: m.cfg.Difficulty, Completed: !res.Incomplete}
	for _, p := range res.Players {
		sc.Players = append(sc.Players, queue.Player{Name: p.Name, Score: p.Score})
	}
	if err := m.store.RecordScore(entry.ID, sc); err != nil {
		return err.Error()
	}
	if err := m.store.SetState(entry.ID, queue.Sung); err != nil {
		return err.Error()
	}
	return ""
}

// winners returns the indexes of the top scorers, or none when there is
// nothing to decide: a single singer, or nobody scoring.
func winners(res live.Results) []int {
	if len(res.Players) < 2 {
		return nil
	}
	top := 0
	for _, p := range res.Players {
		top = max(top, p.Score)
	}
	if top == 0 {
		return nil
	}
	var w []int
	for i, p := range res.Players {
		if p.Score == top {
			w = append(w, i)
		}
	}
	return w
}

// lineStart says when line i starts, or a dash when there is no such line.
func lineStart(lines []lyrics.Line, i int) string {
	if i < 0 || i >= len(lines) {
		return "–"
	}
	return clock(lines[i].Start)
}

func (m karaokeModel) resultsView() string {
	r := m.results
	var b strings.Builder
	b.WriteString("\n  " + ui.Heading.Render(i18n.Tf("karaoke.tui.results.title", entryName(r.entry))) + "\n\n")

	win := map[int]bool{}
	var names []string
	for _, i := range winners(r.res) {
		win[i] = true
		names = append(names, r.res.Players[i].Name)
	}

	row := func(label string, cell func(i int) string) {
		b.WriteString("  " + ui.Muted.Render(ui.Pad(label, 16)))
		for i := range r.res.Players {
			b.WriteString(ui.Pad(cell(i), resultsColumn))
		}
		b.WriteString("\n")
	}
	row("", func(i int) string {
		name := ui.Heading.Render(r.res.Players[i].Name)
		if win[i] {
			name += " " + ui.OK.Render("★")
		}
		return name
	})
	row(i18n.T("karaoke.tui.results.score"), func(i int) string {
		style := ui.Accent.Bold(true)
		if win[i] {
			style = ui.OK.Bold(true)
		}
		return style.Render(fmt.Sprintf("%d", r.res.Players[i].Score))
	})
	row(i18n.T("karaoke.tui.results.in_tune"), func(i int) string {
		return fmt.Sprintf("%.0f%%", r.res.Players[i].InTunePercent)
	})
	row(i18n.T("karaoke.tui.results.mean_error"), func(i int) string {
		return i18n.Tf("karaoke.tui.results.cents", r.res.Players[i].MeanAbsCents)
	})
	row(i18n.T("karaoke.tui.results.tendency"), func(i int) string {
		return i18n.T("karaoke.tendency." + r.res.Players[i].Tendency.String())
	})
	row(i18n.T("karaoke.tui.results.streak"), func(i int) string {
		return fmt.Sprintf("%.1fs", r.res.Players[i].LongestStreak.Round(100*time.Millisecond).Seconds())
	})
	row(i18n.T("karaoke.tui.results.best"), func(i int) string { return lineStart(r.lines, r.res.Players[i].BestLine) })
	row(i18n.T("karaoke.tui.results.worst"), func(i int) string { return lineStart(r.lines, r.res.Players[i].WorstLine) })

	if len(names) > 0 {
		b.WriteString("\n  " + ui.OK.Render(i18n.Tf("karaoke.tui.results.winner", strings.Join(names, ", "))) + "\n")
	}
	if r.res.Incomplete {
		b.WriteString("\n  " + ui.Warning(i18n.T("karaoke.tui.results.incomplete")) + "\n")
	}
	if r.saveErr != "" {
		b.WriteString("\n  " + ui.Warning(i18n.Tf("karaoke.tui.results.save_failed", r.saveErr)) + "\n")
	}
	b.WriteString("\n  " + ui.Muted.Render(i18n.T("karaoke.tui.results.keys")) + "\n")
	return b.String()
}
