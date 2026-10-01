package cli

import (
	"image"
	"math"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Big text is drawn with quadrant block characters: the text is rasterised in
// Go Bold, which covers the accents of Portuguese, onto a grid of two pixels
// per terminal column and four per row, so a pixel is square on a cell twice
// as tall as it is wide. Each cell then shows the 2×2 quadrants whose two
// pixels are mostly ink.
const (
	bigPxPerCol = 2
	bigPxPerRow = 4
	// bigMinRows is the smallest big line worth drawing; below it the text is
	// left at the terminal's size.
	bigMinRows = 3
	// bigMaxLines is how many big rows a line of text may wrap onto.
	bigMaxLines = 3
)

// quadrants maps the ink of a cell's four quadrants, top-left 1, top-right 2,
// bottom-left 4 and bottom-right 8, to the character showing them.
var quadrants = [16]rune{' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛', '▗', '▚', '▐', '▜', '▄', '▙', '▟', '█'}

// bigRow is one wrapped row of big text: its cells, and for every rune it
// holds, the column its glyph ends at, which is where a karaoke wipe that has
// sung it stops.
type bigRow struct {
	cells   [][]rune
	from    int   // index of the row's first rune in the normalised text
	runeEnd []int // per rune of the row, the column after its glyph
}

// bigBlock is a whole line of text laid out in big rows, centred in width.
type bigBlock struct {
	width int
	runes int // runes in the normalised text, the spaces between rows included
	rows  []bigRow
}

var bigFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(gobold.TTF) })

// bigCache keeps the last few layouts: the sing view redraws thirty times a
// second, and the current and next lines only change a few times a minute.
var bigCache = struct {
	sync.Mutex
	order []bigKey
	block map[bigKey]*bigBlock
}{block: map[bigKey]*bigBlock{}}

type bigKey struct {
	text       string
	cols, rows int
}

const bigCacheSize = 8

// layoutBig lays text out as large as it fits in cols × rows, wrapping on
// spaces onto at most bigMaxLines rows. ok is false when even the smallest
// size does not fit, or there is nothing to draw.
func layoutBig(text string, cols, rows int) (*bigBlock, bool) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || cols <= 0 || rows < bigMinRows {
		return nil, false
	}
	key := bigKey{text, cols, rows}
	bigCache.Lock()
	defer bigCache.Unlock()
	if b, ok := bigCache.block[key]; ok {
		return b, b != nil
	}
	b := fitBig(text, cols, rows)
	if len(bigCache.order) >= bigCacheSize {
		delete(bigCache.block, bigCache.order[0])
		bigCache.order = bigCache.order[1:]
	}
	bigCache.order = append(bigCache.order, key)
	bigCache.block[key] = b
	return b, b != nil
}

// fitBig tries sizes from the tallest the rows allow down to bigMinRows.
func fitBig(text string, cols, rows int) *bigBlock {
	f, err := bigFont()
	if err != nil {
		return nil
	}
	words := strings.Split(text, " ")
	for lineRows := rows; lineRows >= bigMinRows; lineRows-- {
		size := bigFontSize(lineRows)
		face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return nil
		}
		lines, ok := wrapBig(face, words, cols*bigPxPerCol)
		if ok && len(lines) <= bigMaxLines && len(lines)*lineRows <= rows {
			b := drawBig(face, lines, cols, lineRows)
			_ = face.Close()
			return b
		}
		_ = face.Close()
	}
	return nil
}

// bigFontSize is the size whose ascent plus descent fills lineRows rows.
func bigFontSize(lineRows int) float64 {
	return float64(lineRows*bigPxPerRow) / bigLineHeight()
}

// bigLineHeight is the font's ascent plus descent per unit of size, measured
// once on a large face where rounding does not matter.
var bigLineHeight = sync.OnceValue(func() float64 {
	const probe = 100
	f, err := bigFont()
	if err != nil {
		return 1
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: probe, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return 1
	}
	defer face.Close()
	m := face.Metrics()
	return float64(m.Ascent+m.Descent) / 64 / probe
})

// wrapBig fills rows greedily with words; ok is false when a single word is
// wider than the space on its own.
func wrapBig(face font.Face, words []string, widthPx int) ([]string, bool) {
	var lines []string
	cur := ""
	for _, w := range words {
		try := w
		if cur != "" {
			try = cur + " " + w
		}
		if font.MeasureString(face, try).Ceil() <= widthPx {
			cur = try
			continue
		}
		if cur == "" {
			return nil, false
		}
		lines = append(lines, cur)
		cur = w
		if font.MeasureString(face, cur).Ceil() > widthPx {
			return nil, false
		}
	}
	return append(lines, cur), true
}

// drawBig rasterises each wrapped line and turns it into quadrant cells.
func drawBig(face font.Face, lines []string, cols, lineRows int) *bigBlock {
	b := &bigBlock{width: cols}
	ascent := face.Metrics().Ascent
	from := 0
	for _, line := range lines {
		runes := []rune(line)
		w := font.MeasureString(face, line).Ceil()
		img := image.NewAlpha(image.Rect(0, 0, w, lineRows*bigPxPerRow))
		d := &font.Drawer{Dst: img, Src: image.Opaque, Face: face, Dot: fixed.Point26_6{Y: ascent}}
		ends := make([]int, len(runes))
		prev := rune(-1)
		for i, r := range runes {
			if prev >= 0 {
				d.Dot.X += face.Kern(prev, r)
			}
			d.DrawString(string(r))
			ends[i] = int(math.Round(float64(d.Dot.X) / 64 / bigPxPerCol))
			prev = r
		}

		rowCols := (w + bigPxPerCol - 1) / bigPxPerCol
		pad := max(0, (cols-rowCols)/2)
		row := bigRow{from: from, runeEnd: make([]int, len(runes))}
		for i, e := range ends {
			row.runeEnd[i] = pad + e
		}
		for y := range lineRows {
			cells := make([]rune, cols)
			for x := range cells {
				cells[x] = ' '
			}
			for x := range rowCols {
				if pad+x < cols {
					cells[pad+x] = quadrantAt(img, x, y)
				}
			}
			row.cells = append(row.cells, cells)
		}
		b.rows = append(b.rows, row)
		from += len(runes) + 1 // the space the wrap replaced
	}
	b.runes = from - 1
	return b
}

// quadrantAt picks the character for cell (x, y) of img.
func quadrantAt(img *image.Alpha, x, y int) rune {
	ink := func(px, py int) bool {
		a := int(img.AlphaAt(px, py).A) + int(img.AlphaAt(px, py+1).A)
		return a >= 255
	}
	px, py := x*bigPxPerCol, y*bigPxPerRow
	bits := 0
	if ink(px, py) {
		bits |= 1
	}
	if ink(px+1, py) {
		bits |= 2
	}
	if ink(px, py+2) {
		bits |= 4
	}
	if ink(px+1, py+2) {
		bits |= 8
	}
	return quadrants[bits]
}

// render draws the block with the first sung runes in the sung style, a
// karaoke wipe that runs along the glyphs, and the rest in the plain style.
func (b *bigBlock) render(sung int, sungStyle, plainStyle lipgloss.Style) []string {
	var out []string
	for _, row := range b.rows {
		wipe := 0 // columns of this row already sung
		switch n := sung - row.from; {
		case n >= len(row.runeEnd):
			wipe = b.width
		case n > 0:
			wipe = row.runeEnd[n-1]
		}
		for _, cells := range row.cells {
			out = append(out, styleSpan(string(cells[:min(wipe, len(cells))]), sungStyle)+
				styleSpan(string(cells[min(wipe, len(cells)):]), plainStyle))
		}
	}
	return out
}

// styleSpan styles s, leaving it bare when it is only spaces so the frame
// does not carry escape codes for nothing.
func styleSpan(s string, st lipgloss.Style) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	return st.Render(s)
}

// bigLines renders text big within cols × rows, falling back to the text at
// the terminal's size, centred, when it cannot be drawn big. progress is the
// fraction of the line that has been sung.
func bigLines(text string, cols, rows int, progress float64, sungStyle, plainStyle lipgloss.Style) []string {
	if b, ok := layoutBig(text, cols, rows); ok {
		return b.render(int(math.Round(progress*float64(b.runes))), sungStyle, plainStyle)
	}
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) == 0 {
		return nil
	}
	n := min(max(int(math.Round(progress*float64(len(r)))), 0), len(r))
	pad := strings.Repeat(" ", max(0, (cols-len(r))/2))
	return []string{pad + styleSpan(string(r[:n]), sungStyle) + styleSpan(string(r[n:]), plainStyle)}
}
