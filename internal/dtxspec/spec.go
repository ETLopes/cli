// Package dtxspec encodes the audio requirements of the Yamaha DTX-PRO drum
// trigger module. Everything the module is picky about lives here, so the rest
// of the program never has to hardcode a sample rate or guess at a filename.
//
// Requirements are taken from the DTX-PRO Owner's/Reference Manual:
//   - Audio files are 16-bit, 44.1 kHz, stereo WAV (linear PCM).
//   - The module can only display alphanumeric characters in file names.
//   - Audio songs must live in the root directory of the USB flash drive;
//     files inside folders are not recognized.
//   - A maximum of 1,000 ".wav" files are handled.
//   - Recording/playback tops out at 90 minutes per file.
package dtxspec

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Audio format required by the module.
const (
	SampleRate = 44100       // Hz
	BitDepth   = 16          // bits per sample
	Channels   = 2           // stereo
	Codec      = "pcm_s16le" // ffmpeg encoder for 16-bit little-endian PCM
	Container  = "wav"
)

// Module limits.
const (
	// MaxSongDuration is the longest audio song the module handles per file.
	MaxSongDuration = 90 * time.Minute
	// MaxWavFiles is how many .wav files the module will enumerate.
	MaxWavFiles = 1000
	// MaxNameLength caps generated file names. The manuals document the
	// alphanumeric restriction but no length limit, so this is a readability
	// choice rather than a device constraint: long enough that a song name
	// plus its "NoVocals" suffix survives intact, short enough to stay
	// scannable on the module's display.
	MaxNameLength = 32
)

// BytesPerSecond is the size of one second of conforming audio, useful for
// estimating output size before any encoding happens.
const BytesPerSecond = SampleRate * Channels * BitDepth / 8

// Format describes the audio properties of a file, as reported by ffprobe.
type Format struct {
	Codec      string
	SampleRate int
	Channels   int
	Duration   time.Duration
}

// Conforms reports whether f is already playable by the module as-is, meaning a
// re-encode would be wasted work.
func (f Format) Conforms() bool {
	return f.Codec == Codec &&
		f.SampleRate == SampleRate &&
		f.Channels == Channels
}

// Deviations lists, in human-readable form, every way f falls short of the
// module's requirements. It returns nil when f conforms.
func (f Format) Deviations() []string {
	var out []string
	if f.Codec != Codec {
		out = append(out, fmt.Sprintf("codec %s (need %s)", orUnknown(f.Codec), Codec))
	}
	if f.SampleRate != SampleRate {
		out = append(out, fmt.Sprintf("%d Hz (need %d Hz)", f.SampleRate, SampleRate))
	}
	if f.Channels != Channels {
		out = append(out, fmt.Sprintf("%d channel(s) (need %d)", f.Channels, Channels))
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// ExceedsDuration reports whether d is longer than the module will play back in
// a single file. Callers should warn rather than fail: an over-long file still
// converts fine, the module just will not play all of it.
func ExceedsDuration(d time.Duration) bool { return d > MaxSongDuration }

// SanitizeName converts an arbitrary title into a name the module can display.
//
// The module only renders alphanumeric characters, so accents are folded to
// their ASCII base (so "Ação" becomes "Acao" rather than "Ao"), word boundaries
// are turned into camel case to stay readable without separators, and anything
// still non-alphanumeric is dropped. A leading digit is preserved, but a name
// that sanitizes down to nothing falls back to "Track".
func SanitizeName(raw string) string {
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		raw,
	)
	if err != nil {
		folded = raw
	}

	var b strings.Builder
	upperNext := true
	for _, r := range folded {
		switch {
		case isApostrophe(r):
			// An apostrophe sits inside a word, so it is dropped without
			// starting a new one: "What's Up" reads as "WhatsUp".
			continue
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			if upperNext {
				b.WriteRune(unicode.ToUpper(r))
				upperNext = false
			} else {
				b.WriteRune(r)
			}
		default:
			// Any non-alphanumeric rune acts as a word boundary and is dropped.
			upperNext = true
		}
	}

	name := b.String()
	if name == "" {
		return "Track"
	}
	return truncateRunes(name, MaxNameLength)
}

// truncateRunes trims to at most n runes. Sanitized names are pure ASCII by
// construction, but operating on runes keeps this correct if that ever changes.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func isApostrophe(r rune) bool {
	return r == '\'' || r == '\u2019' || r == '\u02bc' || r == '`'
}

// noisePattern matches the production boilerplate video titles carry. It is
// deliberately narrow: "(Live)", "(Acoustic)" and "(Remix)" change how a track
// should be played and are kept, while "(Official Music Video)" is 21 of the
// 24 characters the module can show and tells a drummer nothing.
var noisePattern = regexp.MustCompile(`(?i)[\[(][^\[\]()]*\b(?:official|lyrics?|music\s*video|visuali[sz]er|audio\s*only|full\s*video|hd|hq|4k|8k|remaster(?:ed)?)\b[^\[\]()]*[\])]`)

// trailingNoisePattern matches the same boilerplate when it trails without
// brackets, e.g. "Song Title - Official Video".
var trailingNoisePattern = regexp.MustCompile(`(?i)\s*[-–|]\s*(?:official\s*(?:music\s*)?(?:video|audio)|lyrics?(?:\s*video)?|music\s*video|visuali[sz]er)\s*$`)

// CleanTitle removes video-production boilerplate from a title so the part
// worth reading survives truncation to the module's short name limit.
func CleanTitle(raw string) string {
	out := noisePattern.ReplaceAllString(raw, " ")
	out = trailingNoisePattern.ReplaceAllString(out, "")
	out = strings.Join(strings.Fields(out), " ")
	if strings.TrimSpace(SanitizeName(out)) == "" || SanitizeName(out) == "Track" {
		// Never let cleaning throw away the whole title.
		return raw
	}
	return out
}

// Namer produces file names for a related set of outputs that all share one
// base, so they line up on the module's display and differ only in the suffix
// that matters. Shrinking each name independently would yield
// "SomeSongTitlNoVocals" beside "SomeSongTitleNoBass", which is far harder to
// scan on a small screen.
type Namer struct{ base string }

// NewNamer returns a Namer for title, reserving room for the longest of
// variants so every name in the set keeps its distinguishing suffix intact.
func NewNamer(title string, variants []string) *Namer {
	longest := 0
	for _, v := range variants {
		if n := len([]rune(sanitizedVariant(v))); n > longest {
			longest = n
		}
	}
	base := SanitizeName(CleanTitle(title))
	if room := MaxNameLength - longest; room > 0 {
		base = truncateRunes(base, room)
	} else {
		base = ""
	}
	return &Namer{base: base}
}

// Name returns the module-safe file name for one variant.
func (n *Namer) Name(variant string) string {
	name := n.base + sanitizedVariant(variant)
	if name == "" {
		name = "Track"
	}
	return name + "." + Container
}

// sanitizedVariant sanitizes a suffix, mapping the empty case to "" rather
// than the "Track" fallback a bare name would get.
func sanitizedVariant(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	s := SanitizeName(v)
	if s == "Track" {
		return ""
	}
	return s
}

// FormatDescription renders the module's required audio format for display.
func FormatDescription() string {
	return fmt.Sprintf("%.1f kHz · %d-bit · stereo WAV", float64(SampleRate)/1000, BitDepth)
}
