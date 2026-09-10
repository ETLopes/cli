package audio

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ETLopes/cli/internal/dtxspec"
)

// Encoding describes one output format: how ffmpeg should encode it, what to
// call the file, and which folder it belongs in.
//
// Each encoding gets its own folder beside the module-ready WAVs, so a single
// folder can be zipped and sent without picking files apart.
type Encoding struct {
	// ID is the name used in flags and config, e.g. "opus".
	ID string
	// Label is a short human description including the quality setting.
	Label string
	// Extension is the file suffix, without a dot.
	Extension string
	// Dir is the folder the files are written to, relative to the track root.
	Dir string
	// Lossless reports whether decoding reproduces the source exactly.
	Lossless bool
	// Note explains the trade-off, shown in help and the picker.
	Note string

	// args fully specify the encode, including the container.
	args []string
}

// Args returns the ffmpeg encoder arguments for this encoding.
func (e Encoding) Args() []string { return append([]string(nil), e.args...) }

// Filename returns base re-suffixed for this encoding.
func (e Encoding) Filename(base string) string {
	return strings.TrimSuffix(base, "."+dtxspec.Container) + "." + e.Extension
}

// Encoding IDs.
const (
	EncodingWAV  = "wav"
	EncodingFLAC = "flac"
	EncodingOpus = "opus"
	EncodingAAC  = "m4a"
	EncodingMP3  = "mp3"
)

// opusBitrate is transparent for full-range music at stereo. Opus is the most
// efficient codec in general use, so this buys roughly a 40x reduction over
// WAV with no audible difference.
const opusBitrate = "128k"

// aacBitrate is set high because ffmpeg's native AAC encoder is weaker than
// libfdk_aac, which is rarely available in distributed builds. 256k leaves
// enough headroom to stay transparent regardless.
const aacBitrate = "256k"

// encodings is the registry, in presentation order: lossless first, then
// lossy from smallest to most compatible.
var encodings = []Encoding{
	{
		ID: EncodingWAV, Label: "WAV 44.1 kHz / 16-bit", Extension: dtxspec.Container,
		Dir: "dtx", Lossless: true,
		Note: "what the DTX-PRO plays; always produced",
		args: []string{
			"-ar", strconv.Itoa(dtxspec.SampleRate),
			"-ac", strconv.Itoa(dtxspec.Channels),
			"-c:a", dtxspec.Codec,
			"-f", dtxspec.Container,
		},
	},
	{
		ID: EncodingFLAC, Label: "FLAC lossless", Extension: "flac",
		Dir: "flac", Lossless: true,
		Note: "bit-identical to the WAV, about half the size; safe to re-mix",
		args: []string{
			"-ac", strconv.Itoa(dtxspec.Channels),
			// Level 8 is the densest setting; it costs encode time only, and
			// never accuracy, since FLAC is lossless at every level.
			"-c:a", "flac", "-compression_level", "8",
			"-f", "flac",
		},
	},
	{
		ID: EncodingOpus, Label: "Opus " + opusBitrate, Extension: "opus",
		Dir: "opus", Lossless: false,
		Note: "smallest; transparent for listening, lossy if stems get re-mixed",
		args: []string{
			"-ac", strconv.Itoa(dtxspec.Channels),
			// Opus works at 48 kHz internally and resamples on its own, so no
			// sample rate is forced here.
			"-c:a", "libopus", "-b:a", opusBitrate,
			"-vbr", "on", "-application", "audio",
			"-f", "opus",
		},
	},
	{
		ID: EncodingAAC, Label: "AAC " + aacBitrate, Extension: "m4a",
		Dir: "m4a", Lossless: false,
		Note: "plays on essentially any device, including older phones and car stereos",
		args: []string{
			"-ac", strconv.Itoa(dtxspec.Channels),
			"-c:a", "aac", "-b:a", aacBitrate,
			// faststart moves the index to the front so the file streams
			// before it has fully downloaded.
			"-movflags", "+faststart",
			"-f", "ipod",
		},
	},
	{
		ID: EncodingMP3, Label: "MP3 V0 (~245k VBR)", Extension: "mp3",
		Dir: "mp3", Lossless: false,
		Note: "universal fallback; V0 is transparent and smaller than 320k CBR",
		args: []string{
			"-ac", strconv.Itoa(dtxspec.Channels),
			// V0 is variable-rate and beats 320k CBR on size at the same
			// perceived quality.
			"-c:a", "libmp3lame", "-q:a", "0",
			"-f", "mp3",
		},
	},
}

// Encodings returns every supported encoding, in presentation order.
func Encodings() []Encoding { return append([]Encoding(nil), encodings...) }

// LookupEncoding finds an encoding by ID.
func LookupEncoding(id string) (Encoding, bool) {
	for _, e := range encodings {
		if strings.EqualFold(e.ID, id) {
			return e, true
		}
	}
	return Encoding{}, false
}

// EncodingIDs lists every supported encoding ID.
func EncodingIDs() []string {
	ids := make([]string, len(encodings))
	for i, e := range encodings {
		ids[i] = e.ID
	}
	return ids
}

// ResolveEncodings turns a list of IDs into encodings, in registry order and
// with duplicates removed.
//
// WAV is always included: it is what the module plays, and producing anything
// else without it would defeat the point of the tool.
func ResolveEncodings(ids []string) ([]Encoding, error) {
	want := map[string]bool{EncodingWAV: true}
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if _, ok := LookupEncoding(id); !ok {
			return nil, fmt.Errorf("unknown format %q; choose from %s",
				raw, strings.Join(EncodingIDs(), ", "))
		}
		want[id] = true
	}

	var out []Encoding
	for _, e := range encodings {
		if want[e.ID] {
			out = append(out, e)
		}
	}
	return out, nil
}

// SortEncodings orders encodings as the registry presents them.
func SortEncodings(in []Encoding) {
	index := make(map[string]int, len(encodings))
	for i, e := range encodings {
		index[e.ID] = i
	}
	sort.Slice(in, func(i, j int) bool { return index[in[i].ID] < index[in[j].ID] })
}
