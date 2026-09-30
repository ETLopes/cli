package ultrastar

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/lyrics"
)

// parsed is the result of the test-only round-trip parser.
type parsed struct {
	headers map[string]string
	notes   []ChartNote // Beat, Length, Pitch, Text
	breaks  []int
	ended   bool
}

func parseChart(t *testing.T, data []byte) parsed {
	t.Helper()
	p := parsed{headers: map[string]string{}}
	text := string(data)
	if !strings.HasSuffix(text, "E\n") {
		t.Fatalf("file does not end with E and a newline: %q", text[max(0, len(text)-10):])
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			k, v, _ := strings.Cut(line[1:], ":")
			p.headers[k] = v
		case strings.HasPrefix(line, ": "):
			f := strings.SplitN(line[2:], " ", 4)
			if len(f) != 4 {
				t.Fatalf("bad note line %q", line)
			}
			n := ChartNote{Text: f[3]}
			for i, dst := range []*int{&n.Beat, &n.Length, &n.Pitch} {
				v, err := strconv.Atoi(f[i])
				if err != nil {
					t.Fatalf("bad number in %q", line)
				}
				*dst = v
			}
			p.notes = append(p.notes, n)
		case strings.HasPrefix(line, "- "):
			v, err := strconv.Atoi(line[2:])
			if err != nil {
				t.Fatalf("bad break line %q", line)
			}
			p.breaks = append(p.breaks, v)
		case line == "E":
			p.ended = true
		default:
			t.Fatalf("unexpected line %q", line)
		}
	}
	return p
}

// seq builds n 100 ms notes, gapMS apart, starting at zero, on the default
// timing.
func seq(n, gapMS int) []ChartNote {
	var notes []Note
	for i := 0; i < n; i++ {
		s := time.Duration(i*(100+gapMS)) * time.Millisecond
		notes = append(notes, Note{Start: s, End: s + 100*time.Millisecond, Pitch: i})
	}
	return ToChart(notes, DefaultTiming())
}

func line(startMS, endMS int, text string) lyrics.Line {
	return lyrics.Line{Start: time.Duration(startMS) * time.Millisecond, End: time.Duration(endMS) * time.Millisecond, Text: text}
}

func texts(p []Phrase) [][]string {
	var out [][]string
	for _, ph := range p {
		var row []string
		for _, n := range ph.Notes {
			row = append(row, n.Text)
		}
		out = append(out, row)
	}
	return out
}

func TestArrangeGivesThreeWordsToThreeNotes(t *testing.T) {
	ph, had := Arrange(seq(3, 0), []lyrics.Line{line(0, 1000, "la la la")})
	want := [][]string{{"la ", "la ", "la"}}
	if !had || !reflect.DeepEqual(texts(ph), want) {
		t.Errorf("got %q had=%v, want %q", texts(ph), had, want)
	}
}

func TestArrangeFillsExtraNotesWithContinuation(t *testing.T) {
	ph, _ := Arrange(seq(5, 0), []lyrics.Line{line(0, 1000, "one two")})
	got := texts(ph)[0]
	// 2.5 notes' worth of time lies between the words; the tie goes to the
	// earlier split, so "one" covers 2 notes and "two" 3. The space rides on
	// the word's last note.
	want := []string{"one", "~ ", "two", "~", "~"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestArrangeWeightsWordsByNoteDuration(t *testing.T) {
	notes := []Note{
		{Start: 0, End: 500 * time.Millisecond},
		{Start: 500 * time.Millisecond, End: 600 * time.Millisecond},
		{Start: 600 * time.Millisecond, End: 700 * time.Millisecond},
	}
	ph, _ := Arrange(ToChart(notes, DefaultTiming()), []lyrics.Line{line(0, 1000, "one two")})
	want := []string{"one ", "two", "~"}
	if got := texts(ph)[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestArrangeJoinsExtraWordsOntoTheLastNote(t *testing.T) {
	ph, _ := Arrange(seq(2, 0), []lyrics.Line{line(0, 1000, "one two three four five")})
	want := []string{"one ", "two three four five"}
	if got := texts(ph)[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestArrangeSendsNotesOutsideEveryLineToTheNearestLine(t *testing.T) {
	// Notes start at 0, 100, ... 500 ms. Lines cover 100-300 and 400-450.
	// The note at 0 is nearest line one; 300 is nearest line one (its end is
	// exclusive); 500 is nearest line two.
	lines := []lyrics.Line{line(100, 300, "aa bb x"), line(400, 450, "cc dd")}
	ph, _ := Arrange(seq(6, 0), lines)
	if len(ph) != 2 {
		t.Fatalf("phrases = %d", len(ph))
	}
	if len(ph[0].Notes) != 4 || len(ph[1].Notes) != 2 {
		t.Errorf("notes per phrase = %d, %d; want 4, 2", len(ph[0].Notes), len(ph[1].Notes))
	}
}

func TestArrangeDropsLinesWithoutNotes(t *testing.T) {
	lines := []lyrics.Line{line(0, 250, "aa bb"), line(5000, 6000, "nothing here")}
	// Both lines are candidates; the notes all sit near line one.
	ph, _ := Arrange(seq(2, 0), lines)
	if len(ph) != 1 {
		t.Errorf("phrases = %d, want 1", len(ph))
	}
}

func TestArrangeIgnoresBlankLines(t *testing.T) {
	lines := []lyrics.Line{line(0, 150, "aa"), line(150, 300, ""), line(300, 1000, "bb")}
	// The note at 200 ms starts inside the blank line, so it goes to the
	// nearer sung line ("aa", 51 ms away rather than 100 ms).
	ph, _ := Arrange(seq(4, 0), lines)
	if len(ph) != 2 {
		t.Fatalf("phrases = %d", len(ph))
	}
	if len(ph[0].Notes) != 3 || ph[0].Notes[0].Text != "aa" {
		t.Errorf("first phrase = %+v", ph[0])
	}
}

func TestArrangeWithoutLyricsUsesContinuationAndSplitsOnGapsOf400ms(t *testing.T) {
	notes := append(seq(2, 0), func() []ChartNote {
		return ToChart([]Note{{Start: 1000 * time.Millisecond, End: 1100 * time.Millisecond}, {Start: 1500 * time.Millisecond, End: 1600 * time.Millisecond}}, DefaultTiming())
	}()...)
	ph, had := Arrange(notes, nil)
	if had {
		t.Error("hadLyrics = true")
	}
	// Gaps: 200->1000 (800 ms) splits; 1100->1500 (400 ms) splits.
	if len(ph) != 3 {
		t.Fatalf("phrases = %d, want 3", len(ph))
	}
	for _, p := range ph {
		for _, n := range p.Notes {
			if n.Text != "~" {
				t.Errorf("text = %q", n.Text)
			}
		}
	}
	// 399 ms does not split.
	close := ToChart([]Note{{Start: 0, End: 100 * time.Millisecond}, {Start: 499 * time.Millisecond, End: 600 * time.Millisecond}}, DefaultTiming())
	if ph, _ := Arrange(close, nil); len(ph) != 1 {
		t.Errorf("a 399 ms gap split into %d phrases", len(ph))
	}
}

func sampleChart() Chart {
	notes := seq(4, 50)
	ph, _ := Arrange(notes, []lyrics.Line{line(0, 350, "één two"), line(350, 2000, "three fôur")})
	return Chart{
		Header: Header{Title: "Título", Artist: "Artist", Audio: "a.wav", Vocals: "v.wav", Instrumental: "i.wav", Timing: DefaultTiming()},
		Phrases: ph,
	}
}

func TestRenderRoundTripsHeadersNotesAndBreaks(t *testing.T) {
	c := sampleChart()
	p := parseChart(t, c.Render())

	wantHeaders := map[string]string{
		"VERSION": "1.1.0", "TITLE": "Título", "ARTIST": "Artist", "AUDIO": "a.wav",
		"BPM": "1500", "GAP": "0", "VOCALS": "v.wav", "INSTRUMENTAL": "i.wav", "CREATOR": "cli karaoke",
	}
	if !reflect.DeepEqual(p.headers, wantHeaders) {
		t.Errorf("headers = %v", p.headers)
	}
	var want []ChartNote
	for _, ph := range c.Phrases {
		want = append(want, ph.Notes...)
	}
	if len(p.notes) != len(want) {
		t.Fatalf("notes = %d, want %d", len(p.notes), len(want))
	}
	for i, n := range p.notes {
		w := want[i]
		if n.Beat != w.Beat || n.Length != w.Length || n.Pitch != w.Pitch || n.Text != w.Text {
			t.Errorf("note %d = %+v, want %+v", i, n, w)
		}
	}
	if len(p.breaks) != len(c.Phrases)-1 {
		t.Errorf("breaks = %v", p.breaks)
	}
	if !p.ended {
		t.Error("no E")
	}
}

func TestRenderIsUTF8WithoutBOMWithUnixLineEndings(t *testing.T) {
	out := sampleChart().Render()
	if bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("BOM present")
	}
	if bytes.Contains(out, []byte("\r")) {
		t.Error("CR present")
	}
	if !bytes.HasSuffix(out, []byte("\nE\n")) {
		t.Errorf("does not end with E: %q", out[len(out)-6:])
	}
	if !strings.Contains(string(out), "één") {
		t.Error("non-ASCII text was not written as UTF-8")
	}
}

func TestRenderPlacesBreaksBetweenTheEndAndNextStartOfNeighbouringNotes(t *testing.T) {
	c := sampleChart()
	p := parseChart(t, c.Render())
	prev := c.Phrases[0].Notes[len(c.Phrases[0].Notes)-1]
	next := c.Phrases[1].Notes[0]
	if b := p.breaks[0]; b < prev.Beat+prev.Length || b > next.Beat {
		t.Errorf("break %d outside [%d, %d]", b, prev.Beat+prev.Length, next.Beat)
	}
}

func TestBreakBeatIsCappedAfterLongInstrumentals(t *testing.T) {
	prev := ChartNote{Beat: 0, Length: 10}
	next := ChartNote{Beat: 5010, Length: 10}
	if got := breakBeat(prev, next); got != 10+maxBreakLead {
		t.Errorf("break = %d", got)
	}
	if got := breakBeat(prev, ChartNote{Beat: 10}); got != 10 {
		t.Errorf("adjacent break = %d", got)
	}
}

func TestRenderFlattensNewlinesInHeaders(t *testing.T) {
	c := sampleChart()
	c.Header.Title = "Two\nLines"
	p := parseChart(t, c.Render())
	if p.headers["TITLE"] != "Two Lines" {
		t.Errorf("title = %q", p.headers["TITLE"])
	}
}

func TestRenderNegativePitchesAreWrittenWithMinus(t *testing.T) {
	c := Chart{Header: Header{Timing: DefaultTiming()}, Phrases: []Phrase{{Notes: []ChartNote{{Beat: 0, Length: 5, Pitch: -7, Text: "~"}}}}}
	if !strings.Contains(string(c.Render()), fmt.Sprintf(": 0 5 -7 ~\n")) {
		t.Errorf("output: %s", c.Render())
	}
}
