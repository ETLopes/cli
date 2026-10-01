package cli

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/live"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/ui"
)

// resultsColumn is the width of one player's column in the results table.
const resultsColumn = 16

type resultsState struct {
	entry   queue.Entry
	res     live.Results
	lines   []lyrics.Line
	saveErr string
	reveal  revealState
}

// The final scores are revealed one singer at a time, the best last: a
// moment of suspense, a count up that slows as it nears the score, and the
// score held with a verdict before the next singer's turn.
const (
	revealFrame   = 33 * time.Millisecond
	revealIntro   = 1500 * time.Millisecond
	revealCount   = 4 * time.Second
	revealHold    = 2 * time.Second
	revealPerTurn = revealIntro + revealCount + revealHold
	// revealRows is how tall the big score is drawn, terminal permitting.
	revealRows = 12
)

type revealState struct {
	order   []int         // players in reveal order, lowest score first
	turn    int           // index into order; len(order) once all are shown
	start   time.Time     // when the current turn began
	elapsed time.Duration // into the current turn, as of the last tick
}

type revealTickMsg struct{}

func revealTick() tea.Cmd {
	return tea.Tick(revealFrame, func(time.Time) tea.Msg { return revealTickMsg{} })
}

// points100 is a score on the 0–100 scale players see, where 100 is the most
// a song can give. It rounds down, so only a flawless song shows 100.
func points100(points int) int {
	return min(max(points*100/int(score.MaxScore), 0), 100)
}

// startReveal orders the players for the reveal and starts the first turn.
func (m *karaokeModel) startReveal() tea.Cmd {
	players := m.results.res.Players
	order := make([]int, len(players))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return players[a].Score - players[b].Score })
	m.results.reveal = revealState{order: order, start: m.now()}
	if len(order) == 0 {
		return nil
	}
	return revealTick()
}

func (r revealState) done() bool { return r.turn >= len(r.order) }

func (m karaokeModel) revealTick() (tea.Model, tea.Cmd) {
	r := &m.results.reveal
	if m.screen != screenResults || r.done() {
		return m, nil
	}
	r.elapsed = m.now().Sub(r.start)
	if r.elapsed >= revealPerTurn {
		r.turn++
		r.start, r.elapsed = m.now(), 0
		if r.done() {
			return m, nil
		}
	}
	return m, revealTick()
}

// resultsKey skips the reveal on the first press and leaves on the next.
func (m karaokeModel) resultsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "space", " ", "esc", "q":
	default:
		return m, nil
	}
	if !m.results.reveal.done() {
		m.results.reveal.turn = len(m.results.reveal.order)
		return m, nil
	}
	if msg.String() != "space" && msg.String() != " " {
		m.screen = screenQueue
	}
	return m, nil
}

// countUp is the number shown while counting towards final: fast at first,
// crawling over the last few points.
func countUp(final int, elapsed time.Duration) int {
	t := min(max(float64(elapsed)/float64(revealCount), 0), 1)
	return int(math.Floor(float64(final) * (1 - math.Pow(1-t, 4))))
}

// verdict is the line shown with a score, and the style it is drawn in.
func verdict(points int) (string, lipgloss.Style) {
	switch {
	case points >= 100:
		return i18n.T("karaoke.tui.verdict.perfect"), ui.OK.Bold(true)
	case points >= 90:
		return i18n.T("karaoke.tui.verdict.outstanding"), ui.OK.Bold(true)
	case points >= 75:
		return i18n.T("karaoke.tui.verdict.great"), ui.OK
	case points >= 50:
		return i18n.T("karaoke.tui.verdict.good"), ui.Accent
	case points >= 25:
		return i18n.T("karaoke.tui.verdict.fair"), ui.Warn
	}
	return i18n.T("karaoke.tui.verdict.fun"), ui.Warn
}

// revealPhase is where a turn of the reveal is.
type revealPhase int

const (
	phaseSuspense revealPhase = iota // the name, and nothing yet
	phaseCounting                    // the number counting up
	phaseLanded                      // the score and its verdict
)

// revealShown is what the reveal shows right now.
type revealShown struct {
	player int // index into the results' players
	phase  revealPhase
	number int // the number on screen, on the 0–100 scale
}

func (m karaokeModel) revealNow() revealShown {
	r := m.results.reveal
	i := r.order[r.turn]
	final := points100(m.results.res.Players[i].Score)
	switch {
	case r.elapsed < revealIntro:
		return revealShown{player: i, phase: phaseSuspense}
	case r.elapsed < revealIntro+revealCount:
		return revealShown{player: i, phase: phaseCounting, number: countUp(final, r.elapsed-revealIntro)}
	}
	return revealShown{player: i, phase: phaseLanded, number: final}
}

// revealView is the current turn: the suspense line, then the big number.
func (m karaokeModel) revealView(b *strings.Builder) {
	r := m.results.reveal
	shown := m.revealNow()
	p := m.results.res.Players[shown.player]

	for _, i := range r.order[:r.turn] {
		prev := m.results.res.Players[i]
		pts := points100(prev.Score)
		text, style := verdict(pts)
		b.WriteString("  " + ui.Heading.Render(prev.Name) + "  " + style.Render(fmt.Sprintf("%d", pts)) +
			"  " + ui.Muted.Render(text) + "\n")
	}
	if r.turn > 0 {
		b.WriteString("\n")
	}

	dots := strings.Repeat(".", min(3, 1+int(r.elapsed/(revealIntro/3))))
	b.WriteString("  " + centred(ui.Heading.Render(i18n.Tf("karaoke.tui.reveal.intro", p.Name)+dots), m.width-4) + "\n\n")

	rows := min(revealRows, max(bigMinRows, m.revealHeight()))
	cols := m.width - 4
	switch shown.phase {
	case phaseSuspense:
		for range rows {
			b.WriteString("\n")
		}
	case phaseCounting:
		for _, l := range bigLines(fmt.Sprintf("%d", shown.number), cols, rows, 1, ui.Accent.Bold(true), ui.Accent) {
			b.WriteString("  " + l + "\n")
		}
	case phaseLanded:
		text, style := verdict(shown.number)
		for _, l := range bigLines(fmt.Sprintf("%d", shown.number), cols, rows, 1, style, style) {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("\n  " + centred(style.Render(text), cols) + "\n")
	}
	b.WriteString("\n  " + ui.Muted.Render(i18n.T("karaoke.tui.reveal.keys")) + "\n")
}

// revealHeight is the room the big number has under the banner and the
// lines already revealed.
func (m karaokeModel) revealHeight() int {
	h := m.height
	if h <= 0 {
		h = singDefaultHeight
	}
	return h - 12 - m.results.reveal.turn
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
	// Decided on the scale players see, so two equal numbers are a tie.
	top := 0
	for _, p := range res.Players {
		top = max(top, points100(p.Score))
	}
	if top == 0 {
		return nil
	}
	var w []int
	for i, p := range res.Players {
		if points100(p.Score) == top {
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
	if !r.reveal.done() {
		m.revealView(&b)
		return b.String()
	}
	m.finalScoreView(&b)

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
		return style.Render(fmt.Sprintf("%d", points100(r.res.Players[i].Score)))
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

// finalScoreView draws the top score big once the reveal is over, with its
// verdict, above the table.
func (m karaokeModel) finalScoreView(b *strings.Builder) {
	players := m.results.res.Players
	if len(players) == 0 {
		return
	}
	top := 0
	for _, p := range players {
		top = max(top, points100(p.Score))
	}
	text, style := verdict(top)
	rows := min(revealRows, max(bigMinRows, m.revealHeight()-len(players)-10))
	for _, l := range bigLines(fmt.Sprintf("%d", top), m.width-4, rows, 1, style, style) {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n  " + centred(style.Render(text), m.width-4) + "\n\n")
}

// centred pads a styled string so it sits in the middle of cols columns.
func centred(s string, cols int) string {
	return strings.Repeat(" ", max(0, (cols-lipgloss.Width(s))/2)) + s
}
