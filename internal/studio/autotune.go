package studio

import (
	"fmt"
	"sort"
	"strings"
)

// Pitch correction is modelled here rather than left to the plugin's window,
// because the settings that decide how it sounds -- which notes are legal, and
// how fast the voice is dragged onto them -- are the whole effect.

// noteOrder is the order the plugin stores notes in, one bit each.
var noteOrder = []string{"A", "A#", "B", "C", "C#", "D", "D#", "E", "F", "F#", "G", "G#"}

// enharmonics lets a caller write Bb rather than A#.
var enharmonics = map[string]string{
	"BB": "A#", "DB": "C#", "EB": "D#", "GB": "F#", "AB": "G#",
}

// Scale is a set of semitone offsets from the root.
type Scale struct {
	ID   string
	Name string
	// Steps are semitones above the root that belong to the scale.
	Steps []int
}

// scales are the ones worth offering. Chromatic is included but is rarely what
// anyone wants for an obvious effect: with every semitone legal, a voice only
// ever moves to the nearest one, which is a small and unremarkable correction.
var scales = []Scale{
	{ID: "minor", Name: "Minor", Steps: []int{0, 2, 3, 5, 7, 8, 10}},
	{ID: "major", Name: "Major", Steps: []int{0, 2, 4, 5, 7, 9, 11}},
	{ID: "pentatonicminor", Name: "Minor Pentatonic", Steps: []int{0, 3, 5, 7, 10}},
	{ID: "pentatonicmajor", Name: "Major Pentatonic", Steps: []int{0, 2, 4, 7, 9}},
	{ID: "blues", Name: "Blues", Steps: []int{0, 3, 5, 6, 7, 10}},
	{ID: "chromatic", Name: "Chromatic", Steps: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
}

// Scales returns every available scale.
func Scales() []Scale { return append([]Scale(nil), scales...) }

// LookupScale finds a scale by ID or name.
func LookupScale(id string) (Scale, bool) {
	want := normalizeID(id)
	for _, s := range scales {
		if normalizeID(s.ID) == want || normalizeID(s.Name) == want {
			return s, true
		}
	}
	return Scale{}, false
}

// ScaleIDs lists the available scales, for help and errors.
func ScaleIDs() []string {
	out := make([]string, len(scales))
	for i, s := range scales {
		out[i] = s.ID
	}
	return out
}

// Keys lists the twelve roots.
func Keys() []string { return append([]string(nil), noteOrder...) }

// noteIndex resolves a key name to its bit position.
func noteIndex(key string) (int, bool) {
	k := strings.ToUpper(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "♯", "#")
	k = strings.ReplaceAll(k, "♭", "B")
	if alt, ok := enharmonics[k]; ok {
		k = alt
	}
	for i, n := range noteOrder {
		if n == k {
			return i, true
		}
	}
	return 0, false
}

// Retune speed bounds, in milliseconds.
const (
	// RetuneInstant snaps with no transition at all. This is what makes the
	// correction audible rather than invisible: the voice jumps between notes
	// instead of sliding, and the jump is the effect.
	RetuneInstant = 0
	// RetuneNatural is roughly where correction stops being noticeable.
	RetuneNatural = 250
	// MaxRetune is as slow as the plugin is offered here.
	MaxRetune = 1000
)

// Tuning is a pitch-correction setting.
type Tuning struct {
	// Enabled turns correction on.
	Enabled bool
	// Key is the root note, e.g. "A".
	Key string
	// Scale is the scale ID, e.g. "minor".
	Scale string
	// RetuneMs is how long the voice takes to reach the corrected pitch.
	RetuneMs int
	// Depth is the wet/dry mix, 0 to 1. Anything below 1 blends untouched
	// voice underneath, which softens the snap.
	Depth float64
}

// HardTune is the aggressive preset: instant snapping, constrained to a scale,
// fully wet. Every one of those matters -- a slow retune glides, a chromatic
// scale barely moves the note, and a blended dry signal masks the artefact.
func HardTune(key, scale string) Tuning {
	return Tuning{Enabled: true, Key: key, Scale: scale, RetuneMs: RetuneInstant, Depth: 1}
}

// NaturalTune corrects pitch without announcing itself.
func NaturalTune(key, scale string) Tuning {
	return Tuning{Enabled: true, Key: key, Scale: scale, RetuneMs: 40, Depth: 1}
}

// Validate checks a tuning and reports what is wrong in the caller's terms.
func (t Tuning) Validate() error {
	if _, ok := noteIndex(t.Key); !ok {
		return fmt.Errorf("unknown key %q (use one of: %s)", t.Key, strings.Join(noteOrder, " "))
	}
	if _, ok := LookupScale(t.Scale); !ok {
		return fmt.Errorf("unknown scale %q (use one of: %s)", t.Scale, strings.Join(ScaleIDs(), ", "))
	}
	if t.RetuneMs < 0 || t.RetuneMs > MaxRetune {
		return fmt.Errorf("retune speed %d ms is out of range (0 to %d)", t.RetuneMs, MaxRetune)
	}
	if t.Depth < 0 || t.Depth > 1 {
		return fmt.Errorf("depth %.2f is out of range (0 to 1)", t.Depth)
	}
	return nil
}

// NoteMask returns the twelve-bit set of notes correction may snap to, one bit
// per semitone in the plugin's own order.
func (t Tuning) NoteMask() (uint16, error) {
	root, ok := noteIndex(t.Key)
	if !ok {
		return 0, fmt.Errorf("unknown key %q", t.Key)
	}
	sc, ok := LookupScale(t.Scale)
	if !ok {
		return 0, fmt.Errorf("unknown scale %q", t.Scale)
	}
	var mask uint16
	for _, step := range sc.Steps {
		mask |= 1 << uint((root+step)%12)
	}
	return mask, nil
}

// Notes lists the notes correction may snap to, for display.
func (t Tuning) Notes() []string {
	mask, err := t.NoteMask()
	if err != nil {
		return nil
	}
	var out []string
	for i, n := range noteOrder {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}

// Describe renders a tuning in one line.
func (t Tuning) Describe() string {
	if !t.Enabled {
		return "off"
	}
	sc, _ := LookupScale(t.Scale)
	speed := fmt.Sprintf("%d ms", t.RetuneMs)
	if t.RetuneMs == RetuneInstant {
		speed = "instant"
	}
	return fmt.Sprintf("%s %s · retune %s · depth %.0f%%",
		strings.ToUpper(t.Key), sc.Name, speed, t.Depth*100)
}

// MatchMask names the key and scale a note mask represents, so a setting read
// back from the plugin can be described rather than shown as a number. It
// returns the chromatic scale when nothing matches, since that is what a mask
// with every bit set means anyway.
func MatchMask(mask uint16) (key, scale string) {
	for _, root := range noteOrder {
		for _, sc := range scales {
			t := Tuning{Key: root, Scale: sc.ID}
			if m, err := t.NoteMask(); err == nil && m == mask {
				return root, sc.ID
			}
		}
	}
	return "A", "chromatic"
}
