// Package ui renders everything the user sees: styled summaries, interactive
// prompts, and a live view of pipeline progress. It also degrades to plain
// line-based output when there is no terminal to draw on.
package ui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

// Out is the writer commands should print through. It is a colour-profile-aware
// writer, so styled output is downsampled or stripped automatically when stdout
// is a pipe or a terminal with limited colour support.
var Out = lipgloss.Writer

// isDark reports the terminal's background. Querying a terminal costs a round
// trip, so it is only attempted when stdout is actually a terminal; elsewhere
// the styles are stripped on the way out and the answer does not matter.
var isDark = func() bool {
	if !term.IsTerminal(os.Stdout.Fd()) {
		return true
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}()

// lightDark picks between a light- and dark-background variant of a colour.
var lightDark = lipgloss.LightDark(isDark)

// Palette.
var (
	colorAccent = lightDark(lipgloss.Color("#7C3AED"), lipgloss.Color("#A78BFA"))
	colorOK     = lightDark(lipgloss.Color("#047857"), lipgloss.Color("#34D399"))
	colorWarn   = lightDark(lipgloss.Color("#B45309"), lipgloss.Color("#FBBF24"))
	colorErr    = lightDark(lipgloss.Color("#DC2626"), lipgloss.Color("#F87171"))
	colorMuted  = lightDark(lipgloss.Color("#6B7280"), lipgloss.Color("#9CA3AF"))
)

// BarColors are the gradient stops for progress bars.
var BarColors = []color.Color{colorAccent, lipgloss.Color("#EC4899")}

// Shared text styles.
var (
	Title   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	Heading = lipgloss.NewStyle().Bold(true)
	Muted   = lipgloss.NewStyle().Foreground(colorMuted)
	OK      = lipgloss.NewStyle().Foreground(colorOK)
	Warn    = lipgloss.NewStyle().Foreground(colorWarn)
	Err     = lipgloss.NewStyle().Foreground(colorErr)
	Accent  = lipgloss.NewStyle().Foreground(colorAccent)
)

// Status glyphs.
const (
	GlyphOK      = "✓"
	GlyphFail    = "✗"
	GlyphWarn    = "!"
	GlyphPending = "·"
	GlyphBullet  = "•"
)

// Banner renders the application header.
func Banner(subtitle string) string {
	b := Title.Render("dtx")
	if subtitle != "" {
		b += "  " + Muted.Render(subtitle)
	}
	return b
}

// Success renders a positive status line.
func Success(msg string) string { return OK.Render(GlyphOK) + " " + msg }

// Failure renders a negative status line.
func Failure(msg string) string { return Err.Render(GlyphFail) + " " + msg }

// Warning renders a cautionary status line.
func Warning(msg string) string { return Warn.Render(GlyphWarn) + " " + msg }

// Pending renders a not-yet-started status line.
func Pending(msg string) string { return Muted.Render(GlyphPending + " " + msg) }

// KeyValue renders an aligned "label  value" pair, padding label to width.
func KeyValue(label, value string, width int) string {
	return Muted.Render(Pad(label, width)) + "  " + value
}

// Pad right-pads s with spaces to the given display width.
func Pad(s string, width int) string {
	if lipgloss.Width(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-lipgloss.Width(s))
}

// Printf writes styled output through the colour-aware writer.
func Printf(format string, a ...any) { fmt.Fprintf(Out, format, a...) }

// Println writes a styled line through the colour-aware writer.
func Println(a ...any) { fmt.Fprintln(Out, a...) }

// HumanBytes formats a byte count in the largest unit that keeps it readable.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
