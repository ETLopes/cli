package song

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/youtube"
)

// Event reports preparation progress. It has the same shape as pipeline.Event
// so a UI can render both alike; it is its own type because the stages differ.
type Event struct {
	Stage  Stage
	Detail string
	// Fraction is progress within the current stage.
	Fraction float64
	Done     bool
	// Cached marks a Done event for a stage that was already complete and so
	// did no work.
	Cached bool
	// Warning carries a non-fatal problem, such as a lyrics lookup that failed.
	Warning string
}

// Observer receives progress events. It may be called from any goroutine and
// must not block.
type Observer func(Event)

// StageError reports which stage failed, so a queue can show "failed at
// separation" and a retry knows nothing before it needs redoing.
type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string { return fmt.Sprintf("%s stage: %v", e.Stage, e.Err) }
func (e *StageError) Unwrap() error { return e.Err }

// stemDirName is the folder for Demucs output under the song dir. The
// separator adds its own mode and model levels beneath it.
const stemDirName = "stems"

// twoStem is the stem Demucs isolates; the complement is "no_" + twoStem.
const twoStem = "vocals"

// Preparer turns URLs into song directories. Its collaborators sit on the same
// runner.Runner seam as the rest of the toolbox, so tests substitute one fake
// beneath all of them.
type Preparer struct {
	YouTube   *youtube.Client
	Separator *separate.Separator
	Audio     *audio.Converter
	Reference reference.Extractor
	// Lyrics may be nil, which uses the public LRCLIB API.
	Lyrics *lyrics.Client

	// Model is the Demucs model. Empty means separate.ModelDefault.
	Model string
	// Device is the Demucs compute backend. Empty means auto.
	Device string
	// Now supplies timestamps. Nil means time.Now; tests pin it.
	Now func() time.Time
}

func (p *Preparer) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Preparer) model() string {
	if p.Model != "" {
		return p.Model
	}
	return separate.ModelDefault
}

// outcome is what a stage's work reports back to the driver.
type outcome struct {
	artifacts []string
	detail    string
	// unrecorded leaves the stage out of the manifest so the next run retries
	// it. Used for failures that must not block the song but are worth retrying.
	unrecorded bool
}

// run holds the state of one Prepare call.
type run struct {
	p    *Preparer
	ctx  context.Context
	emit func(Event)
	dir  string
	m    Manifest
}

// Prepare produces the song for url under songsDir and returns it. Stages
// already complete on disk are skipped without running any external command;
// a stage that fails leaves the earlier ones recorded, so the next call
// resumes where this one stopped. Cancelling ctx aborts the run the same way.
//
// A completed song is recognised from the URL alone (the video ID is in it),
// so preparing a song again costs neither a yt-dlp call nor an HTTP one.
func (p *Preparer) Prepare(ctx context.Context, url, songsDir string, observe Observer) (Song, error) {
	r := &run{p: p, ctx: ctx, emit: func(e Event) {
		if observe != nil {
			observe(e)
		}
	}}

	if err := r.open(url, songsDir); err != nil {
		return Song{}, err
	}

	steps := []struct {
		stage Stage
		do    func() (outcome, error)
	}{
		{StageDownload, r.download},
		{StageSeparate, r.separate},
		{StageRender, r.render},
		{StageReference, r.reference},
		{StageLyrics, r.lyrics},
	}
	for _, s := range steps {
		if err := r.step(s.stage, s.do); err != nil {
			return Song{}, err
		}
	}
	return Song{Dir: r.dir, Manifest: r.m}, nil
}

// open resolves the song directory and manifest, running Inspect only when the
// URL alone cannot find a prepared song.
func (r *run) open(url, songsDir string) error {
	if id, ok := VideoID(url); ok {
		if dir := findDir(songsDir, id); dir != "" {
			if m, err := readManifest(dir); err == nil && m.VideoID == id && m.complete(dir, StageInspect) {
				r.dir, r.m = dir, m
				r.emit(Event{Stage: StageInspect, Detail: m.Title, Fraction: 1, Done: true, Cached: true})
				return nil
			}
		}
	}

	r.emit(Event{Stage: StageInspect})
	info, err := r.p.YouTube.Inspect(r.ctx, url)
	if err != nil {
		return &StageError{Stage: StageInspect, Err: err}
	}

	dir := findDir(songsDir, info.ID)
	if dir == "" {
		dir = filepath.Join(songsDir, slug(info.Title, info.ID))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &StageError{Stage: StageInspect, Err: fmt.Errorf("creating song directory: %w", err)}
	}

	// A manifest without a recorded inspect (impossible via this code, but a
	// hand-edited or older file) is rebuilt rather than trusted.
	if m, err := readManifest(dir); err == nil && m.VideoID == info.ID && m.complete(dir, StageInspect) {
		r.dir, r.m = dir, m
	} else {
		track, artist := lyrics.CleanTitle(info.Title, info.Uploader)
		r.dir = dir
		r.m = Manifest{
			Version:    ManifestVersion,
			VideoID:    info.ID,
			URL:        info.URL,
			Title:      info.Title,
			Artist:     artist,
			Track:      track,
			DurationMS: info.Duration.Milliseconds(),
			CreatedAt:  r.p.now(),
			Model:      r.p.model(),
			Stages:     map[Stage]StageRecord{},
			Lyrics:     LyricsNone,
		}
		r.m.Stages[StageInspect] = StageRecord{CompletedAt: r.p.now()}
		if err := writeManifest(dir, r.m); err != nil {
			return &StageError{Stage: StageInspect, Err: err}
		}
	}
	r.emit(Event{Stage: StageInspect, Detail: r.m.Title, Fraction: 1, Done: true})
	return nil
}

// step runs one stage unless it is already complete, then records it.
//
// Stages are checked independently and never cascade: redoing an earlier
// stage whose file went missing does not invalidate later ones, because those
// have their own artifacts, verified by their own check. The cost of a
// cascade would be redoing minutes of separation for a deleted download that
// nothing downstream still needs.
func (r *run) step(stage Stage, do func() (outcome, error)) error {
	if r.m.complete(r.dir, stage) {
		r.emit(Event{Stage: stage, Fraction: 1, Done: true, Cached: true})
		return nil
	}
	if err := r.ctx.Err(); err != nil {
		return &StageError{Stage: stage, Err: err}
	}

	r.emit(Event{Stage: stage})
	out, err := do()
	if err != nil {
		return &StageError{Stage: stage, Err: err}
	}
	if out.unrecorded {
		delete(r.m.Stages, stage)
	} else {
		r.m.Stages[stage] = StageRecord{Artifacts: out.artifacts, CompletedAt: r.p.now()}
	}
	if err := writeManifest(r.dir, r.m); err != nil {
		return &StageError{Stage: stage, Err: err}
	}
	if !out.unrecorded {
		r.emit(Event{Stage: stage, Detail: out.detail, Fraction: 1, Done: true})
	}
	return nil
}

// progress adapts a stage-local fraction into an event.
func (r *run) progress(stage Stage, detail string) func(float64) {
	return func(f float64) { r.emit(Event{Stage: stage, Detail: detail, Fraction: f}) }
}

func (r *run) rel(path string) string {
	rel, err := filepath.Rel(r.dir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func (r *run) download() (outcome, error) {
	path, err := r.p.YouTube.Download(r.ctx, r.m.URL, r.dir, r.progress(StageDownload, r.m.Title))
	if err != nil {
		return outcome{}, err
	}
	return outcome{artifacts: []string{r.rel(path)}, detail: filepath.Base(path)}, nil
}

func (r *run) separate() (outcome, error) {
	rec := r.m.Stages[StageDownload]
	if len(rec.Artifacts) == 0 {
		return outcome{}, errors.New("no downloaded audio recorded")
	}
	model := r.p.model()
	source := filepath.Join(r.dir, rec.Artifacts[0])
	stemRoot := filepath.Join(r.dir, stemDirName)
	opts := separate.Options{Model: model, Device: r.p.Device, TwoStems: twoStem}

	arts, err := r.separateOnce(source, stemRoot, opts)
	if err != nil {
		return outcome{}, err
	}
	if arts == nil {
		// The separator reuses whatever WAVs it finds, so a crash between
		// Demucs writing the two stems leaves a directory it takes for a
		// finished run. Clear it and separate for real.
		if err := os.RemoveAll(stemRoot); err != nil {
			return outcome{}, fmt.Errorf("clearing partial stems: %w", err)
		}
		if arts, err = r.separateOnce(source, stemRoot, opts); err != nil {
			return outcome{}, err
		}
		if arts == nil {
			return outcome{}, fmt.Errorf("demucs did not produce both %s.wav and no_%s.wav", twoStem, twoStem)
		}
	}
	r.m.Model = model
	return outcome{artifacts: arts, detail: model}, nil
}

// stem finds a recorded separation artifact by its file name.
func (r *run) stem(name string) (string, error) {
	for _, a := range r.m.Stages[StageSeparate].Artifacts {
		if filepath.Base(a) == name {
			return filepath.Join(r.dir, a), nil
		}
	}
	return "", fmt.Errorf("no %s recorded by separation", name)
}

func (r *run) render() (outcome, error) {
	backing, err := r.stem("no_" + twoStem + ".wav")
	if err != nil {
		return outcome{}, err
	}
	vocals, err := r.stem(twoStem + ".wav")
	if err != nil {
		return outcome{}, err
	}

	// Two files share the stage, so each reports half of its progress.
	half := func(offset float64) audio.ProgressFunc {
		return func(f float64) { r.emit(Event{Stage: StageRender, Fraction: offset + f/2}) }
	}
	total := r.m.Duration()
	if err := r.p.Audio.RenderWAV(r.ctx, backing, filepath.Join(r.dir, InstrumentalName),
		audio.WAVSpec{SampleRate: 48000, Channels: 2, Format: audio.SampleFloat32}, total, half(0)); err != nil {
		return outcome{}, err
	}
	if err := r.p.Audio.RenderWAV(r.ctx, vocals, filepath.Join(r.dir, VocalsName),
		audio.WAVSpec{SampleRate: 16000, Channels: 1, Format: audio.SampleInt16}, total, half(0.5)); err != nil {
		return outcome{}, err
	}
	return outcome{artifacts: []string{InstrumentalName, VocalsName}, detail: "2 files"}, nil
}

func (r *run) reference() (outcome, error) {
	if err := r.p.Reference.Extract(r.ctx, filepath.Join(r.dir, VocalsName), filepath.Join(r.dir, ReferenceName)); err != nil {
		return outcome{}, err
	}
	return outcome{artifacts: []string{ReferenceName}}, nil
}

// lyrics finds lyrics. Nothing that goes wrong here fails the song: the
// worst outcome is singing without a lyric display.
func (r *run) lyrics() (outcome, error) {
	client := r.p.Lyrics
	if client == nil {
		client = &lyrics.Client{}
	}

	// Stale files from an earlier attempt must not outlive a different answer.
	os.Remove(filepath.Join(r.dir, SyncedLyricsName))
	os.Remove(filepath.Join(r.dir, PlainLyricsName))

	// Re-derived rather than trusted, so a song inspected before the title
	// cleaning improved searches with the better guess. The stored artist
	// stands in for the uploader, which is what it came from when the title
	// had no "Artist - " prefix.
	r.m.Track, r.m.Artist = lyrics.CleanTitle(r.m.Title, r.m.Artist)
	found, ok, err := client.Find(r.ctx, r.m.Track, r.m.Artist, r.m.Duration())
	if err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return outcome{}, ctxErr
		}
		// Not recorded: an outage says nothing about whether lyrics exist, so
		// the next run should ask again.
		warning := "lyrics unavailable: " + err.Error()
		r.m.Lyrics, r.m.LyricsNote = LyricsNone, "lookup failed"
		r.emit(Event{Stage: StageLyrics, Warning: warning})
		return outcome{unrecorded: true}, nil
	}

	r.m.LyricsNote = ""
	switch {
	case !ok:
		r.m.Lyrics, r.m.LyricsNote = LyricsNone, "no match"
		return outcome{detail: "none"}, nil
	case found.Instrumental:
		r.m.Lyrics, r.m.LyricsNote = LyricsNone, "instrumental"
		return outcome{detail: "instrumental"}, nil
	case found.HasSynced():
		if err := writeFileAtomic(filepath.Join(r.dir, SyncedLyricsName), []byte(found.SyncedLyrics)); err != nil {
			return outcome{}, err
		}
		r.m.Lyrics = LyricsSynced
		return outcome{artifacts: []string{SyncedLyricsName}, detail: "synced"}, nil
	case strings.TrimSpace(found.PlainLyrics) != "":
		if err := writeFileAtomic(filepath.Join(r.dir, PlainLyricsName), []byte(found.PlainLyrics)); err != nil {
			return outcome{}, err
		}
		r.m.Lyrics = LyricsPlain
		return outcome{artifacts: []string{PlainLyricsName}, detail: "plain"}, nil
	default:
		r.m.Lyrics, r.m.LyricsNote = LyricsNone, "no lyrics"
		return outcome{detail: "none"}, nil
	}
}

// findDir locates an existing song directory for a video ID, whatever title it
// was created under. Titles get edited; the ID is the stable key.
func findDir(songsDir, id string) string {
	if !idPattern.MatchString(id) {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(songsDir, "*-"+id))
	if err != nil {
		return ""
	}
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			return m
		}
	}
	return ""
}

// separateOnce runs the separator and returns the relative paths of the vocal
// stem and its complement, or nil when either is missing from the result.
func (r *run) separateOnce(source, stemRoot string, opts separate.Options) ([]string, error) {
	stems, err := r.p.Separator.Separate(r.ctx, source, stemRoot, opts, r.progress(StageSeparate, opts.Model))
	if err != nil {
		return nil, err
	}
	var arts []string
	for _, want := range []string{twoStem, "no_" + twoStem} {
		for _, s := range stems {
			if s.Name == want {
				arts = append(arts, r.rel(s.Path))
			}
		}
	}
	if len(arts) != 2 {
		return nil, nil
	}
	return arts, nil
}
