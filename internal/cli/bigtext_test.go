package cli

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The styles are told apart by a marker the test can see in plain text.
var (
	sungMark  = lipgloss.NewStyle().Transform(func(s string) string { return "<" + s + ">" })
	plainMark = lipgloss.NewStyle()
)

func TestBigTextFillsTheRowsItIsGivenAndStaysInsideTheWidth(t *testing.T) {
	lines := bigLines("alpha bravo", 120, 8, 0, plainMark, plainMark)
	if len(lines) < bigMinRows || len(lines) > 8 {
		t.Fatalf("%d rows, want between %d and 8", len(lines), bigMinRows)
	}
	for _, l := range lines {
		if w := len([]rune(plain(l))); w > 120 {
			t.Errorf("row is %d columns wide, want at most 120", w)
		}
	}
	if strings.Contains(strings.Join(lines, ""), "alpha") {
		t.Error("big text left the words as plain letters")
	}
}

func TestBigTextWrapsALongLineRatherThanShrinkingItAway(t *testing.T) {
	one := bigLines("alpha bravo charlie delta", 200, 12, 0, plainMark, plainMark)
	narrow := bigLines("alpha bravo charlie delta", 60, 12, 0, plainMark, plainMark)
	b, ok := layoutBig("alpha bravo charlie delta", 60, 12)
	if !ok || len(b.rows) < 2 {
		t.Fatalf("a narrow terminal should wrap: ok=%v", ok)
	}
	if len(narrow) < bigMinRows || len(one) < bigMinRows {
		t.Errorf("rows: wide %d, narrow %d", len(one), len(narrow))
	}
}

func TestBigTextDrawsAccentedLetters(t *testing.T) {
	plainA, _ := layoutBig("a", 40, 8)
	accented, ok := layoutBig("ã", 40, 8)
	if !ok {
		t.Fatal("an accented letter could not be drawn")
	}
	ink := func(b *bigBlock) int {
		n := 0
		for _, r := range b.rows {
			for _, cells := range r.cells {
				n += len(strings.TrimSpace(string(cells)))
			}
		}
		return n
	}
	if ink(accented) <= ink(plainA) {
		t.Error("the tilde drew no ink of its own")
	}
}

func TestTheWipeColoursTheSungPartOfEveryRow(t *testing.T) {
	text := "alpha bravo"
	none := strings.Join(bigLines(text, 120, 6, 0, sungMark, plainMark), "\n")
	all := strings.Join(bigLines(text, 120, 6, 1, sungMark, plainMark), "\n")
	half := bigLines(text, 120, 6, 0.5, sungMark, plainMark)
	if strings.Contains(none, "<") {
		t.Error("nothing sung, yet something is in the sung style")
	}
	if !strings.Contains(all, "<") {
		t.Error("everything sung, yet nothing is in the sung style")
	}
	// Halfway, each row is sung from its start up to a point, never in pieces.
	sungRows, restRows := 0, 0
	for _, l := range half {
		if strings.Count(l, "<") > 1 || (strings.Contains(l, "<") && !strings.HasPrefix(l, "<")) {
			t.Errorf("row %q is not sung from its start", l)
		}
		if strings.Contains(l, "<") {
			sungRows++
		}
		if i := strings.Index(l, ">"); strings.TrimSpace(l[i+1:]) != "" {
			restRows++
		}
	}
	if sungRows == 0 || restRows == 0 {
		t.Errorf("halfway: %d rows with sung ink, %d with unsung ink, want both", sungRows, restRows)
	}
}

func TestTextThatCannotBeDrawnBigFallsBackToOneCentredLine(t *testing.T) {
	lines := bigLines("alpha bravo", 30, 2, 5.0/11, sungMark, plainMark)
	if len(lines) != 1 || !strings.Contains(lines[0], "<alpha>") || !strings.Contains(lines[0], "bravo") {
		t.Errorf("fallback = %q", lines)
	}
	if got := bigLines("   ", 30, 8, 0, sungMark, plainMark); got != nil {
		t.Errorf("blank text drew %q", got)
	}
}
