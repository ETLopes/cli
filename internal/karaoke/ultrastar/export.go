package ultrastar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

// Warning is a non-fatal note about an export.
type Warning string

// WarningNoLyrics says the song has no synced lyrics, so every note is "~"
// and the game shows no words.
const WarningNoLyrics Warning = "no synced lyrics: notes are exported with ~ syllables"

// Result describes a finished export.
type Result struct {
	// Dir is the song folder, <outDir>/<Artist> - <Title>.
	Dir string
	// Chart is the path of the .txt file.
	Chart    string
	Warnings []Warning
}

// vocalStemName is the Demucs vocal stem recorded by the separate stage. The
// export uses it rather than vocals16k.wav, which is 16 kHz mono for analysis.
const vocalStemName = "vocals.wav"

// gridStep is the resolution notes are segmented at: the scorer's grid.
const gridStep = 10 * time.Millisecond

// exportWAV is what all three audio files are rendered as. The source is an
// m4a or webm that UltraStar Deluxe may not decode, whereas WAV always works.
var exportWAV = audio.WAVSpec{SampleRate: 44100, Channels: 2, Format: audio.SampleInt16}

// Export writes an UltraStar Deluxe folder for s under outDir.
//
// The chart is computed first so a song with nothing to sing fails before the
// slow audio renders, and it is written last, atomically, so a .txt in the
// folder means the audio beside it is complete. Rerunning overwrites.
func Export(ctx context.Context, s song.Song, outDir string, conv *audio.Converter) (Result, error) {
	sources, err := audioSources(s)
	if err != nil {
		return Result{}, err
	}

	contour, err := reference.Load(s.ReferencePath())
	if err != nil {
		return Result{}, fmt.Errorf("loading reference for export: %w", err)
	}
	notes := ToChart(Segment(contour.Grid(gridStep), DefaultSegmentConfig()), DefaultTiming())
	if len(notes) == 0 {
		return Result{}, errors.New("no sung notes found in the reference contour")
	}

	var res Result
	l, hasLyrics, err := s.LoadLyrics()
	if err != nil {
		return Result{}, err
	}
	phrases, had := Arrange(notes, l.Lines)
	if !hasLyrics || !had {
		res.Warnings = append(res.Warnings, WarningNoLyrics)
	}

	artist, title := displayNames(s.Manifest)
	base := sanitizeName(artist + " - " + title)
	res.Dir = filepath.Join(outDir, base)
	if err := os.MkdirAll(res.Dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating export folder: %w", err)
	}

	names := struct{ audio, instrumental, vocals string }{
		audio:        base + ".wav",
		instrumental: base + " [INSTR].wav",
		vocals:       base + " [VOC].wav",
	}
	for _, r := range []struct{ src, name string }{
		{sources.mix, names.audio},
		{sources.instrumental, names.instrumental},
		{sources.vocals, names.vocals},
	} {
		if err := conv.RenderWAV(ctx, r.src, filepath.Join(res.Dir, r.name), exportWAV, s.Duration(), nil); err != nil {
			return Result{}, fmt.Errorf("rendering %s: %w", r.name, err)
		}
	}

	chart := Chart{
		Header: Header{
			Title: title, Artist: artist,
			Audio: names.audio, Instrumental: names.instrumental, Vocals: names.vocals,
			Timing: DefaultTiming(),
		},
		Phrases: phrases,
	}
	res.Chart = filepath.Join(res.Dir, base+".txt")
	if err := writeFileAtomic(res.Chart, chart.Render()); err != nil {
		return Result{}, err
	}
	return res, nil
}

type audioPaths struct{ mix, instrumental, vocals string }

// audioSources locates the three inputs from what the manifest recorded.
func audioSources(s song.Song) (audioPaths, error) {
	var p audioPaths
	dl := s.Stages[song.StageDownload].Artifacts
	if len(dl) == 0 {
		return p, errors.New("song has no downloaded audio recorded")
	}
	p.mix = filepath.Join(s.Dir, dl[0])
	p.instrumental = s.InstrumentalPath()
	for _, a := range s.Stages[song.StageSeparate].Artifacts {
		if filepath.Base(a) == vocalStemName {
			p.vocals = filepath.Join(s.Dir, a)
		}
	}
	if p.vocals == "" {
		return p, fmt.Errorf("song has no separated %s recorded", vocalStemName)
	}
	for _, f := range []string{p.mix, p.instrumental, p.vocals} {
		if info, err := os.Stat(f); err != nil || info.Size() == 0 {
			return p, fmt.Errorf("audio file %s is missing or empty", filepath.Base(f))
		}
	}
	return p, nil
}

// displayNames are the artist and title written to the chart: the cleaned
// track and artist from the manifest, falling back to the video title.
func displayNames(m song.Manifest) (artist, title string) {
	title = strings.TrimSpace(m.Track)
	if title == "" {
		title = strings.TrimSpace(m.Title)
	}
	artist = strings.TrimSpace(m.Artist)
	if artist == "" {
		artist = "Unknown Artist"
	}
	return artist, title
}

// maxNameRunes keeps folder and file names comfortably under the 255-byte
// limit of common filesystems even with the suffix and non-ASCII text.
const maxNameRunes = 100

// sanitizeName makes s safe as a file or folder name on macOS, Windows and
// Linux: invalid characters and control characters become "_", whitespace
// collapses, and leading/trailing spaces and trailing dots (which Windows
// rejects) are trimmed.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r), unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if rs := []rune(name); len(rs) > maxNameRunes {
		name = string(rs[:maxNameRunes])
	}
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	name = strings.TrimLeft(name, ". ")
	if name == "" {
		return "song"
	}
	return name
}

// writeFileAtomic replaces path with data so a crash leaves the old file or
// the new one, never a torn one.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), werr)
	}
	return nil
}
