package reaper

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"github.com/ETLopes/cli/internal/studio"
)

// ReaTune exposes only Bypass, Wet and Delta as automatable parameters, so its
// correction settings cannot be reached the ordinary way. They do live in the
// plugin's state blob, which REAPER will hand over and take back, so the
// settings are written by patching that.
//
// The layout below was established by capturing one instance configured for
// instant snapping and another left at defaults, then comparing them byte by
// byte. Seven bytes differed, and three of them carry everything that decides
// how the correction sounds.

//go:embed presets/reatune_correction.bin
var reatuneTemplate []byte

// Byte offsets within ReaTune's state blob.
const (
	// offEnabled holds the automatic-correction switch.
	offEnabled = 0
	// offNoteMask holds twelve bits, one per semitone, marking which notes
	// correction may snap to. Confirmed by reading back a scale-constrained
	// instance: 0x05AD is exactly A B C D E F G.
	offNoteMask = 20
	// offRetune holds the transition time in milliseconds. Zero is what makes
	// the correction audible; the stock 250 is what makes it invisible.
	offRetune = 24
)

// minTemplateSize guards against a truncated template silently producing a
// blob that REAPER would reject or, worse, misread.
const minTemplateSize = 104

// TuningChunk renders a tuning as the base64 state blob REAPER expects.
func TuningChunk(t studio.Tuning) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	if len(reatuneTemplate) < minTemplateSize {
		return "", fmt.Errorf("reatune template is %d bytes, expected at least %d",
			len(reatuneTemplate), minTemplateSize)
	}
	mask, err := t.NoteMask()
	if err != nil {
		return "", err
	}

	blob := make([]byte, len(reatuneTemplate))
	copy(blob, reatuneTemplate)

	var enabled uint32
	if t.Enabled {
		enabled = 1
	}
	binary.LittleEndian.PutUint32(blob[offEnabled:], enabled)
	binary.LittleEndian.PutUint16(blob[offNoteMask:], mask)
	binary.LittleEndian.PutUint32(blob[offRetune:], uint32(t.RetuneMs))

	return base64.StdEncoding.EncodeToString(blob), nil
}

// ReadTuning recovers the settings from a state blob, so what REAPER actually
// holds can be compared against what was asked for.
func ReadTuning(chunk string) (studio.Tuning, error) {
	blob, err := base64.StdEncoding.DecodeString(chunk)
	if err != nil {
		return studio.Tuning{}, fmt.Errorf("decoding plugin state: %w", err)
	}
	if len(blob) < minTemplateSize {
		return studio.Tuning{}, fmt.Errorf("plugin state is %d bytes, expected at least %d",
			len(blob), minTemplateSize)
	}

	t := studio.Tuning{
		Enabled:  binary.LittleEndian.Uint32(blob[offEnabled:]) != 0,
		RetuneMs: int(binary.LittleEndian.Uint32(blob[offRetune:])),
		Depth:    1,
	}
	mask := binary.LittleEndian.Uint16(blob[offNoteMask:])
	t.Key, t.Scale = studio.MatchMask(mask)
	return t, nil
}
