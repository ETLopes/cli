package ultrastar

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/lyrics"
)

const (
	// FormatVersion is the UltraStar txt format the writer targets.
	FormatVersion = "1.1.0"
	// Creator is written to #CREATOR.
	Creator = "cli karaoke"
	// Continuation is UltraStar's syllable for "keep holding the previous
	// word", used for extra notes a word is sung over.
	Continuation = "~"

	// unlyricPhraseGap is the silence that ends a phrase when there are no
	// lyrics to say where lines break: about a breath.
	unlyricPhraseGap = 400 * time.Millisecond
	// maxBreakLead caps how far past a phrase's last note its break sits, in
	// beats (1 s at the default timing). Midway across a long instrumental
	// would leave the finished line on screen for ages.
	maxBreakLead = 100
)

// Header is the metadata block of a song file.
type Header struct {
	Title, Artist string
	// Audio, Vocals and Instrumental are file names beside the .txt.
	Audio, Vocals, Instrumental string
	Timing                      Timing
}

// Phrase is a run of notes shown as one lyric line.
type Phrase struct {
	Notes []ChartNote
}

// Chart is a whole song file.
type Chart struct {
	Header  Header
	Phrases []Phrase
}

// Arrange groups notes into phrases and gives them syllables.
//
// With lyrics, each non-blank line owns the notes starting inside
// [Start, End); a note outside every line goes to the nearest one, and a line
// left without notes disappears. A line's words are spread over its notes in
// proportion to note length, and every word gets at least one note. Words
// beyond the note count are joined onto the last note, and notes beyond the
// word count carry "~". A word's trailing space sits on the last note it is
// sung over (the line's last word has none), so the game's lyric bar reads
// naturally.
//
// Without lyrics every note is "~" and phrases split at silences of 400 ms or
// more. hadLyrics reports which case applied.
func Arrange(notes []ChartNote, lines []lyrics.Line) (phrases []Phrase, hadLyrics bool) {
	var sung []lyrics.Line
	for _, l := range lines {
		if !l.Blank() {
			sung = append(sung, l)
		}
	}
	if len(notes) == 0 {
		return nil, len(sung) > 0
	}
	if len(sung) == 0 {
		return arrangeBare(notes), false
	}

	groups := make([][]ChartNote, len(sung))
	for _, n := range notes {
		k := owner(sung, n.Start)
		groups[k] = append(groups[k], n)
	}
	for k, g := range groups {
		if len(g) == 0 {
			continue
		}
		assignWords(g, strings.Fields(sung[k].Text))
		phrases = append(phrases, Phrase{Notes: g})
	}
	return phrases, true
}

// owner picks the line for a note starting at t: the line containing it, else
// the nearest by distance to its span (the earlier line on a tie).
func owner(lines []lyrics.Line, t time.Duration) int {
	best, bestDist := 0, time.Duration(math.MaxInt64)
	for i, l := range lines {
		var d time.Duration
		switch {
		case t < l.Start:
			d = l.Start - t
		case t >= l.End:
			d = t - l.End + 1 // outside: never ties with containment
		}
		if d == 0 {
			return i
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

func arrangeBare(notes []ChartNote) []Phrase {
	var out []Phrase
	var cur []ChartNote
	for i, n := range notes {
		if i > 0 && n.Start-notes[i-1].End >= unlyricPhraseGap {
			out = append(out, Phrase{Notes: cur})
			cur = nil
		}
		n.Text = Continuation
		cur = append(cur, n)
	}
	return append(out, Phrase{Notes: cur})
}

// assignWords sets Text on every note in g from words.
func assignWords(g []ChartNote, words []string) {
	if len(words) == 0 {
		for i := range g {
			g[i].Text = Continuation
		}
		return
	}
	if len(words) >= len(g) {
		// One word per note; the surplus rides on the last note.
		for i := range g {
			g[i].Text = words[i]
		}
		g[len(g)-1].Text = strings.Join(words[len(g)-1:], " ")
	} else {
		// first[j] is the note word j starts on: the one whose start is
		// closest to j/len(words) of the line's sung time, leaving room for
		// the words still to come.
		var total time.Duration
		cum := make([]time.Duration, len(g))
		for i, n := range g {
			cum[i] = total
			total += n.End - n.Start
		}
		first := make([]int, len(words))
		for j := 1; j < len(words); j++ {
			target := total * time.Duration(j) / time.Duration(len(words))
			lo, hi := first[j-1]+1, len(g)-(len(words)-j)
			best := lo
			for i := lo; i <= hi; i++ {
				if abs(cum[i]-target) < abs(cum[best]-target) {
					best = i
				}
			}
			first[j] = best
		}
		for i := range g {
			g[i].Text = Continuation
		}
		for j, w := range words {
			g[first[j]].Text = w
		}
		// Move each word's separating space onto the last note it covers.
		for j := 0; j+1 < len(words); j++ {
			g[first[j+1]-1].Text += " "
		}
		return
	}
	for i := 0; i < len(g)-1; i++ {
		g[i].Text += " "
	}
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// breakBeat is where the phrase ends between prev and next: midway across the
// gap so a finished line lingers a moment, but at most maxBreakLead beats past
// the last note. It never falls before prev's end or after next's start.
func breakBeat(prev, next ChartNote) int {
	end := prev.Beat + prev.Length
	gap := max(0, next.Beat-end)
	return end + min(gap/2, maxBreakLead)
}

// Render encodes the chart as a song file: UTF-8, no BOM, \n line endings,
// ending in "E".
func (c Chart) Render() []byte {
	var b bytes.Buffer
	h := c.Header
	hdr := func(key, val string) { fmt.Fprintf(&b, "#%s:%s\n", key, oneLine(val)) }
	hdr("VERSION", FormatVersion)
	hdr("TITLE", h.Title)
	hdr("ARTIST", h.Artist)
	hdr("AUDIO", h.Audio)
	hdr("BPM", formatNumber(h.Timing.BPM))
	hdr("GAP", formatNumber(h.Timing.GapMS))
	if h.Vocals != "" {
		hdr("VOCALS", h.Vocals)
	}
	if h.Instrumental != "" {
		hdr("INSTRUMENTAL", h.Instrumental)
	}
	hdr("CREATOR", Creator)

	var prev *ChartNote
	for _, p := range c.Phrases {
		if len(p.Notes) == 0 {
			continue
		}
		if prev != nil {
			fmt.Fprintf(&b, "- %d\n", breakBeat(*prev, p.Notes[0]))
		}
		for _, n := range p.Notes {
			fmt.Fprintf(&b, ": %d %d %d %s\n", n.Beat, n.Length, n.Pitch, oneLine(n.Text))
		}
		last := p.Notes[len(p.Notes)-1]
		prev = &last
	}
	b.WriteString("E\n")
	return b.Bytes()
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// oneLine flattens whitespace runs that would break the line-based format.
// Trailing spaces are kept: in a note line they are meaningful.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
}
