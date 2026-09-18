package studio

import (
	"fmt"
	"strings"
)

// A Kind is what sort of instrument is plugged into an input. It decides which
// pedalboard that input gets, which is what lets three guitars on three inputs
// each carry their own amp, drive and delay rather than sharing one.
//
// Kind is deliberately separate from an instrument's identity. Renaming an
// input does not change what is plugged into it, and putting a second guitar
// on input 4 is a change of kind rather than a new concept in the model.
type Kind string

const (
	KindVocal  Kind = "vocal"
	KindGuitar Kind = "guitar"
	KindBass   Kind = "bass"
	KindKeys   Kind = "keys"
	KindDrums  Kind = "drums"
	// KindLine is anything else with a level and a jack: a horn, a DI'd
	// acoustic, a percussion overhead. It gets a plain, useful chain rather
	// than no processing at all, because an input nobody anticipated is still
	// an input somebody has to mix.
	KindLine Kind = "line"
)

// kindInfo is how a kind names the instruments made from it.
type kindInfo struct {
	// Stem is the ID an instrument of this kind takes, e.g. "guitar".
	Stem string
	// Title is the display name, e.g. "Guitar".
	Title string
}

// kindOrder is the order kinds cycle in, commonest first.
var kindOrder = []Kind{KindVocal, KindGuitar, KindBass, KindKeys, KindDrums, KindLine}

var kindInfos = map[Kind]kindInfo{
	KindVocal:  {Stem: "mic", Title: "Mic"},
	KindGuitar: {Stem: "guitar", Title: "Guitar"},
	KindBass:   {Stem: "bass", Title: "Bass"},
	KindKeys:   {Stem: "keyboard", Title: "Keyboard"},
	KindDrums:  {Stem: "drums", Title: "Drums"},
	KindLine:   {Stem: "line", Title: "Line"},
}

// Kinds lists every kind, in cycling order.
func Kinds() []Kind { return append([]Kind(nil), kindOrder...) }

// KindNames lists every kind as written in configuration, for help and errors.
func KindNames() []string {
	out := make([]string, len(kindOrder))
	for i, k := range kindOrder {
		out[i] = string(k)
	}
	return out
}

// Valid reports whether a kind is one this studio knows.
func (k Kind) Valid() bool { _, ok := kindInfos[k]; return ok }

func (k Kind) String() string { return string(k) }

// Next and Prev step through the kinds, wrapping, so changing what is plugged
// into an input is one key rather than a menu.
func (k Kind) Next() Kind { return k.step(1) }

// Prev is Next in the other direction.
func (k Kind) Prev() Kind { return k.step(-1) }

func (k Kind) step(by int) Kind {
	for i, c := range kindOrder {
		if c == k {
			return kindOrder[(i+by+len(kindOrder))%len(kindOrder)]
		}
	}
	return kindOrder[0]
}

// kindAliases are the other words people write for a kind, in the order they
// are tried. A slice rather than a map because two aliases can match the same
// name and the answer must not depend on map iteration order.
var kindAliases = []struct {
	word string
	kind Kind
}{
	{"vocal", KindVocal}, {"vocals", KindVocal}, {"voice", KindVocal},
	{"mic", KindVocal}, {"microphone", KindVocal}, {"vox", KindVocal},
	{"guitar", KindGuitar}, {"gtr", KindGuitar}, {"electricguitar", KindGuitar},
	{"bass", KindBass}, {"bassguitar", KindBass},
	{"keys", KindKeys}, {"keyboard", KindKeys}, {"keyboards", KindKeys},
	{"synth", KindKeys}, {"piano", KindKeys},
	{"drums", KindDrums}, {"drum", KindDrums}, {"kit", KindDrums},
	{"dtx", KindDrums}, {"edrums", KindDrums},
	{"line", KindLine}, {"other", KindLine}, {"aux", KindLine},
}

// ParseKind reads a kind from configuration, accepting the words people
// actually write for one.
func ParseKind(s string) (Kind, error) {
	want := normalizeID(s)
	for _, a := range kindAliases {
		if a.word == want {
			return a.kind, nil
		}
	}
	return "", fmt.Errorf("%q is not an instrument kind; use one of %s",
		s, strings.Join(KindNames(), ", "))
}

// InferKind guesses what an input holds from its ID, so a rig described before
// kinds existed keeps working without being rewritten. A config file naming
// its inputs mic1, guitar and dtx already says what they are.
func InferKind(id string) Kind {
	if k, err := ParseKind(id); err == nil {
		return k
	}
	n := normalizeID(id)
	for _, a := range kindAliases {
		if strings.HasPrefix(n, a.word) {
			return a.kind
		}
	}
	return KindLine
}

// NextInstrument names a new instrument of a kind, given what the studio
// already holds. The first of a kind takes the plain name and later ones are
// numbered, so three guitars read Guitar, Guitar 2, Guitar 3 — a rig that only
// ever has one should not have to call it Guitar 1.
//
// The ID is checked against every instrument, not just those of the same kind,
// because two inputs sharing an identity is the mistake that silently sends
// one musician's effects to another.
func NextInstrument(k Kind, existing []Instrument) (id, name string) {
	info, ok := kindInfos[k]
	if !ok {
		info = kindInfos[KindLine]
	}
	taken := map[string]bool{}
	n := 0
	for _, in := range existing {
		taken[normalizeID(in.ID)] = true
		if in.EffectiveKind() == k {
			n++
		}
	}
	for i := n; ; i++ {
		id, name = info.Stem, info.Title
		if i > 0 {
			id = fmt.Sprintf("%s%d", info.Stem, i+1)
			name = fmt.Sprintf("%s %d", info.Title, i+1)
		}
		if !taken[normalizeID(id)] {
			return id, name
		}
	}
}
