// Package song prepares karaoke songs and describes them on disk.
//
// A song is a directory holding everything a live session and an UltraStar
// export need, plus a song.json manifest saying which preparation stages have
// finished. Preparation is resumable: a run that dies during the (long)
// separation step never repeats the download, and a finished song is loaded
// without running any external tool.
//
// Layout of <songsDir>/<slug>-<videoID>/:
//
//	song.json                                   manifest
//	source.<ext>                                downloaded audio
//	stems/2stems-vocals/<model>/vocals.wav      Demucs vocal stem
//	stems/2stems-vocals/<model>/no_vocals.wav   Demucs backing stem
//	instrumental.wav                            48 kHz stereo float, for playback
//	vocals16k.wav                               16 kHz mono 16-bit, for analysis
//	reference.json                              the original singer's pitch contour
//	lyrics.lrc | lyrics.txt                     synced or plain lyrics, if any
package song

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
)

// Fixed file names inside a song directory.
const (
	ManifestName     = "song.json"
	InstrumentalName = "instrumental.wav"
	VocalsName       = "vocals16k.wav"
	ReferenceName    = "reference.json"
	SyncedLyricsName = "lyrics.lrc"
	PlainLyricsName  = "lyrics.txt"
)

// ManifestVersion is bumped when the manifest changes shape incompatibly.
const ManifestVersion = 1

// Stage is one step of song preparation. The values are stable identifiers
// stored in the manifest; use Label for display.
type Stage string

const (
	StageInspect   Stage = "inspect"
	StageDownload  Stage = "download"
	StageSeparate  Stage = "separate"
	StageRender    Stage = "render"
	StageReference Stage = "reference"
	StageLyrics    Stage = "lyrics"
)

// Stages lists the stages in execution order.
var Stages = []Stage{StageInspect, StageDownload, StageSeparate, StageRender, StageReference, StageLyrics}

// Label is the stage's name in the user's language.
func (s Stage) Label() string { return i18n.T("karaoke.stage." + string(s)) }

// LyricsStatus says what lyrics a song ended up with.
type LyricsStatus string

const (
	// LyricsSynced means lyrics.lrc holds timed lyrics.
	LyricsSynced LyricsStatus = "synced"
	// LyricsPlain means only untimed lyrics.txt exists: readable, not followable.
	LyricsPlain LyricsStatus = "plain"
	// LyricsNone means there are no lyrics: an instrumental, no match, or a
	// lookup that failed. The song can still be sung.
	LyricsNone LyricsStatus = "none"
)

// StageRecord notes a finished stage.
type StageRecord struct {
	// Artifacts are the files the stage produced, relative to the song
	// directory so the songs directory can be moved or synced. A stage with
	// artifacts only counts as complete while every one is still non-empty.
	Artifacts   []string  `json:"artifacts,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

// Manifest is the contents of song.json.
type Manifest struct {
	Version int    `json:"version"`
	VideoID string `json:"video_id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	// Artist and Track are what was searched for, derived from the title and
	// uploader.
	Artist string `json:"artist"`
	Track  string `json:"track"`
	// DurationMS is the video's length. Milliseconds as an integer, not a
	// time.Duration, whose nanosecond encoding is opaque in a file.
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
	// Model is the Demucs model that produced the stems.
	Model  string                `json:"model"`
	Stages map[Stage]StageRecord `json:"stages"`
	Lyrics LyricsStatus          `json:"lyrics"`
	// LyricsNote explains a LyricsNone outcome, for display and debugging.
	LyricsNote string `json:"lyrics_note,omitempty"`
}

// Duration is the video's length.
func (m Manifest) Duration() time.Duration { return time.Duration(m.DurationMS) * time.Millisecond }

// complete reports whether a stage finished: the manifest records it and, for
// stages that produce files, every file is still there and non-empty. The
// manifest alone is not trusted because files are what the next stage reads,
// and a disk cleaner or interrupted copy can remove them behind its back.
func (m Manifest) complete(dir string, s Stage) bool {
	rec, ok := m.Stages[s]
	if !ok {
		return false
	}
	for _, rel := range rec.Artifacts {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || info.IsDir() || info.Size() == 0 {
			return false
		}
	}
	return true
}

// Song is a prepared (or partly prepared) song directory.
type Song struct {
	Dir string
	Manifest
}

// InstrumentalPath is the 48 kHz stereo float backing track.
func (s Song) InstrumentalPath() string { return filepath.Join(s.Dir, InstrumentalName) }

// VocalsPath is the 16 kHz mono vocal stem.
func (s Song) VocalsPath() string { return filepath.Join(s.Dir, VocalsName) }

// ReferencePath is the pitch contour JSON.
func (s Song) ReferencePath() string { return filepath.Join(s.Dir, ReferenceName) }

// Ready reports whether the song can be sung: it needs the backing track and
// the reference to score against. Lyrics are optional.
func (s Song) Ready() bool {
	for _, st := range []Stage{StageRender, StageReference} {
		if !s.complete(s.Dir, st) {
			return false
		}
	}
	return true
}

// LoadLyrics reads the synced lyrics. ok is false when the song has none, which is
// a normal state rather than an error.
func (s Song) LoadLyrics() (l lyrics.Lyrics, ok bool, err error) {
	if s.Manifest.Lyrics != LyricsSynced {
		return lyrics.Lyrics{}, false, nil
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, SyncedLyricsName))
	if err != nil {
		return lyrics.Lyrics{}, false, fmt.Errorf("reading lyrics: %w", err)
	}
	return lyrics.ParseLRC(string(data)), true, nil
}

// Load reads the song in dir.
func Load(dir string) (Song, error) {
	m, err := readManifest(dir)
	if err != nil {
		return Song{}, err
	}
	return Song{Dir: dir, Manifest: m}, nil
}

func readManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("reading song manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parsing song manifest in %s: %w", dir, err)
	}
	if m.Version > ManifestVersion {
		return Manifest{}, fmt.Errorf("song manifest in %s is version %d; this build understands up to %d", dir, m.Version, ManifestVersion)
	}
	if m.Stages == nil {
		m.Stages = map[Stage]StageRecord{}
	}
	return m, nil
}

func writeManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding song manifest: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, ManifestName), append(data, '\n'))
}

// writeFileAtomic replaces path with data so a crash leaves either the old
// contents or the new, never a torn file: write a temp file in the same
// directory (rename is only atomic within a filesystem), fsync it, rename.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	return nil
}

// slug builds a filesystem-friendly folder name.
//
// It is a copy of the unexported slug in internal/pipeline, kept identical so
// karaoke and dtx name folders by one rule. The video ID is appended so two
// videos with the same title never collide and a folder traces back to its
// source.
func slug(title, id string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "track"
	}
	if id != "" {
		return s + "-" + id
	}
	return s
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// VideoID extracts the YouTube video ID from a URL without any network call.
// That lets a finished song be found from its URL alone, so re-adding a song
// already prepared costs no yt-dlp run.
func VideoID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var id string
	switch {
	case host == "youtu.be":
		id = strings.Trim(u.Path, "/")
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		if v := u.Query().Get("v"); v != "" {
			id = v
		} else {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
				id = parts[1]
			}
		}
	}
	if !idPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
