// Package lyrics fetches synced lyrics from LRCLIB and parses the LRC format
// they arrive in.
package lyrics

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lastLineSpan is how long the final line is shown for. LRC records only when
// a line starts, so the last line has no successor to end it; five seconds is
// long enough to sing a line and short enough not to hang on an outro.
const lastLineSpan = 5 * time.Second

// Line is one lyric line, timed to the song.
type Line struct {
	Start time.Duration
	// End is when the next line starts, or Start plus lastLineSpan for the
	// last one.
	End  time.Duration
	Text string
}

// Blank reports whether the line is an instrumental break rather than words.
// LRC files mark a gap with a timestamp and no text, and the display wants to
// know about them (to clear the previous line) without scoring them.
func (l Line) Blank() bool { return strings.TrimSpace(l.Text) == "" }

// Lyrics is a parsed LRC file.
type Lyrics struct {
	Title  string
	Artist string
	// Lines are sorted by Start.
	Lines []Line
}

var (
	// stampPattern matches a leading [mm:ss], [mm:ss.x], [mm:ss.xx] or
	// [mm:ss.xxx] time tag. Requiring two seconds digits rejects the
	// hand-typed "[0:5]" junk that would otherwise be guessed at.
	stampPattern = regexp.MustCompile(`^\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]`)
	// tagPattern matches a [key:value] metadata tag.
	tagPattern = regexp.MustCompile(`^\[([A-Za-z]+):(.*)\]\s*$`)
)

// ParseLRC reads LRC text. It never fails: a lyric file with a few bad lines is
// still worth singing along to, so malformed lines are skipped.
func ParseLRC(text string) Lyrics {
	var (
		out    Lyrics
		offset time.Duration
	)

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" {
			continue
		}

		var stamps []time.Duration
		rest := line
		for {
			m := stampPattern.FindStringSubmatch(rest)
			if m == nil {
				break
			}
			stamps = append(stamps, stampDuration(m[1], m[2], m[3]))
			rest = rest[len(m[0]):]
		}

		if len(stamps) == 0 {
			if m := tagPattern.FindStringSubmatch(line); m != nil {
				value := strings.TrimSpace(m[2])
				switch strings.ToLower(m[1]) {
				case "ar":
					out.Artist = value
				case "ti":
					out.Title = value
				case "offset":
					if ms, err := strconv.Atoi(strings.TrimPrefix(value, "+")); err == nil {
						offset = time.Duration(ms) * time.Millisecond
					}
				}
			}
			continue
		}

		body := strings.TrimSpace(rest)
		for _, s := range stamps {
			out.Lines = append(out.Lines, Line{Start: s, Text: body})
		}
	}

	// The LRC offset is "positive shifts lyrics earlier", hence the
	// subtraction. It is applied after parsing because the tag may appear
	// after some of the lines it affects.
	for i := range out.Lines {
		out.Lines[i].Start = max(out.Lines[i].Start-offset, 0)
	}

	sort.SliceStable(out.Lines, func(i, j int) bool { return out.Lines[i].Start < out.Lines[j].Start })
	for i := range out.Lines {
		if i+1 < len(out.Lines) {
			out.Lines[i].End = out.Lines[i+1].Start
		} else {
			out.Lines[i].End = out.Lines[i].Start + lastLineSpan
		}
	}
	return out
}

// stampDuration converts the captured minutes, seconds and optional fraction.
// The fraction is read as decimal digits, so "5" is half a second, not five
// milliseconds.
func stampDuration(min, sec, frac string) time.Duration {
	m, _ := strconv.Atoi(min)
	s, _ := strconv.Atoi(sec)
	d := time.Duration(m)*time.Minute + time.Duration(s)*time.Second
	if frac != "" {
		f, _ := strconv.Atoi((frac + "00")[:3])
		d += time.Duration(f) * time.Millisecond
	}
	return d
}
