package lyrics

import (
	"testing"
	"time"
)

func TestParseLRCReadsATimestampedLine(t *testing.T) {
	l := ParseLRC("[00:12.30]la la la")
	if len(l.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(l.Lines))
	}
	got := l.Lines[0]
	if got.Start != 12300*time.Millisecond || got.Text != "la la la" {
		t.Errorf("got %+v", got)
	}
}

func TestParseLRCExpandsMultipleTimestampsOnOneLine(t *testing.T) {
	l := ParseLRC("[00:12.30][01:02.00]line one")
	if len(l.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(l.Lines))
	}
	if l.Lines[0].Start != 12300*time.Millisecond || l.Lines[1].Start != 62*time.Second {
		t.Errorf("starts = %v, %v", l.Lines[0].Start, l.Lines[1].Start)
	}
	if l.Lines[0].Text != "line one" || l.Lines[1].Text != "line one" {
		t.Errorf("texts = %q, %q", l.Lines[0].Text, l.Lines[1].Text)
	}
}

func TestParseLRCAcceptsEveryFractionWidth(t *testing.T) {
	l := ParseLRC("[00:01]a\n[00:02.5]b\n[00:03.25]c\n[00:04.125]d")
	want := []time.Duration{1000 * time.Millisecond, 2500 * time.Millisecond, 3250 * time.Millisecond, 4125 * time.Millisecond}
	if len(l.Lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(l.Lines), len(want))
	}
	for i, w := range want {
		if l.Lines[i].Start != w {
			t.Errorf("line %d start = %v, want %v", i, l.Lines[i].Start, w)
		}
	}
}

func TestParseLRCAppliesTheOffsetTag(t *testing.T) {
	// A positive offset makes lyrics appear sooner, so it is subtracted.
	l := ParseLRC("[offset:+500]\n[00:10.00]line one")
	if got := l.Lines[0].Start; got != 9500*time.Millisecond {
		t.Errorf("start = %v, want 9.5s", got)
	}
	l = ParseLRC("[offset:-500]\n[00:10.00]line one")
	if got := l.Lines[0].Start; got != 10500*time.Millisecond {
		t.Errorf("start = %v, want 10.5s", got)
	}
}

func TestParseLRCClampsAnOffsetThatWouldGoNegative(t *testing.T) {
	l := ParseLRC("[offset:+5000]\n[00:01.00]line one")
	if got := l.Lines[0].Start; got != 0 {
		t.Errorf("start = %v, want 0", got)
	}
}

func TestParseLRCKeepsMetadataTags(t *testing.T) {
	l := ParseLRC("[ar:Placeholder Artist]\n[ti:Placeholder Title]\n[00:01.00]la la la")
	if l.Artist != "Placeholder Artist" || l.Title != "Placeholder Title" {
		t.Errorf("artist=%q title=%q", l.Artist, l.Title)
	}
	if len(l.Lines) != 1 {
		t.Errorf("metadata tags must not become lines, got %d lines", len(l.Lines))
	}
}

func TestParseLRCSortsUnsortedInput(t *testing.T) {
	l := ParseLRC("[00:30.00]line three\n[00:10.00]line one\n[00:20.00]line two")
	for i, w := range []string{"line one", "line two", "line three"} {
		if l.Lines[i].Text != w {
			t.Errorf("line %d = %q, want %q", i, l.Lines[i].Text, w)
		}
	}
}

func TestParseLRCSkipsMalformedLines(t *testing.T) {
	l := ParseLRC("garbage\n[xx:yy.zz]nope\n[00:5]\n[99]bad\n[00:10.00]line one\nno stamp here")
	if len(l.Lines) != 1 || l.Lines[0].Text != "line one" {
		t.Errorf("got %+v, want only line one", l.Lines)
	}
}

func TestParseLRCKeepsBlankTimedLinesAsBreaks(t *testing.T) {
	l := ParseLRC("[00:10.00]line one\n[00:14.00]\n[00:20.00]line two")
	if len(l.Lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(l.Lines))
	}
	if l.Lines[1].Text != "" || !l.Lines[1].Blank() {
		t.Errorf("middle line should be a blank break, got %+v", l.Lines[1])
	}
}

func TestParseLRCComputesEndTimes(t *testing.T) {
	l := ParseLRC("[00:10.00]line one\n[00:14.00]line two")
	if l.Lines[0].End != 14*time.Second {
		t.Errorf("first End = %v, want 14s", l.Lines[0].End)
	}
	if l.Lines[1].End != 19*time.Second {
		t.Errorf("last End = %v, want start+5s", l.Lines[1].End)
	}
}

func TestParseLRCHandlesWindowsLineEndings(t *testing.T) {
	l := ParseLRC("[00:01.00]la la la\r\n[00:02.00]placeholder words\r\n")
	if len(l.Lines) != 2 || l.Lines[0].Text != "la la la" {
		t.Errorf("got %+v", l.Lines)
	}
}

func TestParseLRCOfEmptyInputHasNoLines(t *testing.T) {
	if l := ParseLRC(""); len(l.Lines) != 0 {
		t.Errorf("got %d lines", len(l.Lines))
	}
}
