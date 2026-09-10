// Package pipeline orchestrates the full journey from a video URL to a set of
// files a DTX-PRO will play: download, separate, render, and optionally export
// to a USB drive.
//
// Every stage reports progress through a single Observer, and every stage is
// resumable -- intermediate artifacts are cached on disk, so a run interrupted
// during rendering does not repeat the multi-minute separation step.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/dtxspec"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/youtube"
)

// Stage identifies a phase of the pipeline.
type Stage string

const (
	StageInspect  Stage = "Inspecting"
	StageDownload Stage = "Downloading"
	StageSeparate Stage = "Separating"
	StageRender   Stage = "Rendering"
	StageEncode   Stage = "Encoding"
	StageExport   Stage = "Exporting"
)

// Stages lists the pipeline's phases in execution order.
var Stages = []Stage{StageInspect, StageDownload, StageSeparate, StageRender, StageEncode, StageExport}

// Event reports pipeline progress. Fraction is within the current stage.
type Event struct {
	Stage    Stage
	Detail   string
	Fraction float64
	Done     bool
	// Warning carries a non-fatal problem worth surfacing, such as a track
	// longer than the module will play.
	Warning string
}

// Observer receives progress events. It may be called from any goroutine and
// must not block.
type Observer func(Event)

// Kind classifies a rendered output file.
type Kind string

const (
	// KindFull is the original recording, unmodified apart from format.
	KindFull Kind = "full"
	// KindMinusOne is the mix with exactly one instrument removed.
	KindMinusOne Kind = "minus-one"
	// KindStem is a single isolated instrument.
	KindStem Kind = "stem"
)

// Output is one file produced for the module.
type Output struct {
	Kind Kind
	// Instrument is the stem a minus-one or stem output relates to. Empty for
	// KindFull.
	Instrument string
	// Encoding is the audio.Encoding ID this file was written in.
	Encoding string
	Path     string
	Bytes    int64
}

// Name is the output's file name.
func (o Output) Name() string { return filepath.Base(o.Path) }

// Request describes one pipeline run.
type Request struct {
	URL string
	// OutputDir is the root under which a per-track folder is created.
	OutputDir string
	// Model and Device are passed through to Demucs.
	Model  string
	Device string
	Shifts int
	Jobs   int
	// Normalize and Limit control post-processing of rendered mixes.
	Normalize bool
	Limit     bool
	// Formats lists the audio.Encoding IDs to produce. WAV is always included
	// regardless, since it is the only format the module plays.
	Formats []string
	// USBPath, when set, is the mount point of a USB drive to copy the
	// finished files onto. Files land in the root, as the module requires.
	USBPath string
	// CookiesFromBrowser is passed to yt-dlp for age-restricted videos.
	CookiesFromBrowser string
}

// Result summarizes a completed run.
type Result struct {
	Info youtube.Info
	// Dir is the per-track folder holding everything produced.
	Dir string
	// DTXDir holds the module-ready files.
	DTXDir string
	// Outputs are the module-ready files, ordered full, minus-one, then stems.
	Outputs []Output
	// Warnings are non-fatal problems encountered during the run.
	Warnings []string
	// Exported is the USB destination when files were copied there.
	Exported string
	Elapsed  time.Duration
}

// EncodingSummary rolls up what was produced in one format.
type EncodingSummary struct {
	Encoding audio.Encoding
	Dir      string
	Count    int
	Bytes    int64
}

// ByEncoding groups the outputs by format, in registry order. The summary
// prints this rather than one line per file: with five formats that would be
// forty-five lines, and the per-format totals are what a user actually needs
// to decide what to send.
func (r Result) ByEncoding() []EncodingSummary {
	index := map[string]int{}
	var out []EncodingSummary
	for _, o := range r.Outputs {
		enc, ok := audio.LookupEncoding(o.Encoding)
		if !ok {
			continue
		}
		i, seen := index[o.Encoding]
		if !seen {
			out = append(out, EncodingSummary{Encoding: enc, Dir: enc.Dir})
			i = len(out) - 1
			index[o.Encoding] = i
		}
		out[i].Count++
		out[i].Bytes += o.Bytes
	}
	sort.Slice(out, func(i, j int) bool {
		return encodingOrder(out[i].Encoding.ID) < encodingOrder(out[j].Encoding.ID)
	})
	return out
}

// encodingOrder gives the registry's presentation order for an encoding ID.
func encodingOrder(id string) int {
	for i, e := range audio.Encodings() {
		if e.ID == id {
			return i
		}
	}
	return len(audio.Encodings())
}

// TotalBytes is the combined size of every produced file.
func (r Result) TotalBytes() int64 {
	var n int64
	for _, o := range r.Outputs {
		n += o.Bytes
	}
	return n
}

// Pipeline runs the conversion. Its collaborators are interfaces-by-struct so
// tests can substitute a fake runner beneath them.
type Pipeline struct {
	YouTube   *youtube.Client
	Audio     *audio.Converter
	Separator *separate.Separator
}

// Run executes the pipeline. Cancelling ctx aborts it; partial artifacts are
// left in place so a later run can resume from them.
func (p *Pipeline) Run(ctx context.Context, req Request, observe Observer) (*Result, error) {
	started := time.Now()
	emit := func(e Event) {
		if observe != nil {
			observe(e)
		}
	}

	// Resolve formats up front. This is pure input validation, so it must
	// happen before the download and separation: a typo in --formats should
	// cost a moment, not the minutes those stages take.
	encodings, err := audio.ResolveEncodings(req.Formats)
	if err != nil {
		return nil, err
	}

	p.YouTube.CookiesFromBrowser = req.CookiesFromBrowser

	// Stage 1: metadata. Cheap, and it names the output folder.
	emit(Event{Stage: StageInspect, Fraction: 0})
	info, err := p.YouTube.Inspect(ctx, req.URL)
	if err != nil {
		return nil, err
	}

	result := &Result{Info: info}
	if dtxspec.ExceedsDuration(info.Duration) {
		w := fmt.Sprintf("track is %s; the module plays at most %s per file",
			formatDuration(info.Duration), formatDuration(dtxspec.MaxSongDuration))
		result.Warnings = append(result.Warnings, w)
		emit(Event{Stage: StageInspect, Warning: w})
	}

	result.Dir = filepath.Join(req.OutputDir, slug(info.Title, info.ID))
	result.DTXDir = filepath.Join(result.Dir, "dtx")
	if err := os.MkdirAll(result.DTXDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}
	emit(Event{Stage: StageInspect, Detail: info.Title, Fraction: 1, Done: true})

	// Stage 2: download the source audio.
	emit(Event{Stage: StageDownload, Detail: info.Title, Fraction: 0})
	source, err := p.YouTube.Download(ctx, req.URL, result.Dir, func(f float64) {
		emit(Event{Stage: StageDownload, Detail: info.Title, Fraction: f})
	})
	if err != nil {
		return nil, err
	}
	emit(Event{Stage: StageDownload, Detail: filepath.Base(source), Fraction: 1, Done: true})

	// Prefer the real duration of the downloaded file: yt-dlp metadata is
	// occasionally rounded or absent, and progress maths depends on it.
	duration := info.Duration
	if format, err := p.Audio.Probe(ctx, source); err == nil && format.Duration > 0 {
		duration = format.Duration
	}

	// Stage 3: separation, by far the most expensive step.
	emit(Event{Stage: StageSeparate, Detail: modelLabel(req.Model), Fraction: 0})
	stems, err := p.Separator.Separate(ctx, source, filepath.Join(result.Dir, "stems"), separate.Options{
		Model:  req.Model,
		Device: req.Device,
		Shifts: req.Shifts,
		Jobs:   req.Jobs,
	}, func(f float64) {
		emit(Event{Stage: StageSeparate, Detail: modelLabel(req.Model), Fraction: f})
	})
	if err != nil {
		return nil, err
	}
	emit(Event{
		Stage:    StageSeparate,
		Detail:   fmt.Sprintf("%d stems: %s", len(stems), strings.Join(stemNames(stems), ", ")),
		Fraction: 1,
		Done:     true,
	})

	// Stage 4: render everything the module will play.
	opts := audio.Options{Normalize: req.Normalize, Limit: req.Limit}
	outputs, err := p.render(ctx, renderJob{
		source:   source,
		stems:    stems,
		title:    info.Title,
		dtxDir:   result.DTXDir,
		duration: duration,
		opts:     opts,
	}, emit)
	if err != nil {
		return nil, err
	}
	result.Outputs = outputs
	emit(Event{Stage: StageRender, Detail: fmt.Sprintf("%d files", len(outputs)), Fraction: 1, Done: true})

	// Stage 5: derive the shareable copies from the rendered WAVs.
	encoded, err := p.encode(ctx, outputs, encodings, result.Dir, duration, emit)
	if err != nil {
		return nil, err
	}
	result.Outputs = append(result.Outputs, encoded...)
	if len(encoded) > 0 {
		emit(Event{Stage: StageEncode, Detail: fmt.Sprintf("%d files", len(encoded)), Fraction: 1, Done: true})
	} else {
		emit(Event{Stage: StageEncode, Detail: "WAV only", Fraction: 1, Done: true})
	}

	// Stage 6: optional copy to the module's USB drive.
	if req.USBPath != "" {
		emit(Event{Stage: StageExport, Detail: req.USBPath, Fraction: 0})
		warns, err := exportToUSB(result.Outputs, req.USBPath, func(f float64) {
			emit(Event{Stage: StageExport, Detail: req.USBPath, Fraction: f})
		})
		if err != nil {
			return nil, err
		}
		result.Warnings = append(result.Warnings, warns...)
		result.Exported = req.USBPath
		emit(Event{Stage: StageExport, Detail: req.USBPath, Fraction: 1, Done: true})
	}

	result.Elapsed = time.Since(started)
	return result, nil
}

// renderJob groups the inputs to the render stage.
type renderJob struct {
	source   string
	stems    []separate.Stem
	title    string
	dtxDir   string
	duration time.Duration
	opts     audio.Options
}

// render produces the module-ready files: the original, one minus-one mix per
// stem, and each isolated stem.
//
// The minus-one mixes are the point of the exercise -- "NoDrums" is what you
// play along to -- so they are rendered before the isolated stems, and a run
// cancelled midway still leaves the most useful files behind.
func (p *Pipeline) render(ctx context.Context, job renderJob, emit func(Event)) ([]Output, error) {
	type task struct {
		kind       Kind
		instrument string
		variant    string
		inputs     []string
	}

	tasks := []task{{kind: KindFull, variant: "Full", inputs: []string{job.source}}}
	for _, s := range job.stems {
		tasks = append(tasks, task{
			kind:       KindMinusOne,
			instrument: s.Name,
			variant:    "No" + dtxspec.SanitizeName(s.Name),
			inputs:     pathsExcluding(job.stems, s.Name),
		})
	}
	for _, s := range job.stems {
		tasks = append(tasks, task{
			kind:       KindStem,
			instrument: s.Name,
			variant:    dtxspec.SanitizeName(s.Name),
			inputs:     []string{s.Path},
		})
	}

	// Naming is decided for the set as a whole so every file shares one base
	// and differs only in its suffix.
	variants := make([]string, len(tasks))
	for i, t := range tasks {
		variants[i] = t.variant
	}
	namer := dtxspec.NewNamer(job.title, variants)

	outputs := make([]Output, 0, len(tasks))
	for i, t := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := namer.Name(t.variant)
		dst := filepath.Join(job.dtxDir, name)

		// Progress across the whole stage, so the bar advances smoothly rather
		// than resetting for each of the nine or so files.
		base := float64(i) / float64(len(tasks))
		span := 1 / float64(len(tasks))
		onProgress := func(f float64) {
			emit(Event{Stage: StageRender, Detail: name, Fraction: base + f*span})
		}

		var err error
		switch {
		case t.kind == KindStem:
			// Demucs already emits module-compatible WAV; verify rather than
			// blindly re-encode.
			err = p.Audio.EnsureDTX(ctx, t.inputs[0], dst, job.opts, onProgress)
		case len(t.inputs) == 1:
			err = p.Audio.ToDTX(ctx, t.inputs[0], dst, job.duration, job.opts, onProgress)
		default:
			err = p.Audio.Mix(ctx, t.inputs, dst, job.duration, job.opts, onProgress)
		}
		if err != nil {
			return nil, err
		}

		out := Output{Kind: t.kind, Instrument: t.instrument, Encoding: audio.EncodingWAV, Path: dst}
		if info, statErr := os.Stat(dst); statErr == nil {
			out.Bytes = info.Size()
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

// encode derives the shareable copies from the rendered module WAVs. Encoding
// from the finished WAV rather than re-mixing from stems guarantees every
// format carries byte-identical source audio, differing only in codec.
func (p *Pipeline) encode(ctx context.Context, wavs []Output, encodings []audio.Encoding, dir string, duration time.Duration, emit func(Event)) ([]Output, error) {
	var lossy []audio.Encoding
	for _, e := range encodings {
		if e.ID != audio.EncodingWAV {
			lossy = append(lossy, e)
		}
	}
	if len(lossy) == 0 || len(wavs) == 0 {
		return nil, nil
	}

	total := len(lossy) * len(wavs)
	out := make([]Output, 0, total)
	done := 0

	for _, enc := range lossy {
		for _, w := range wavs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := enc.Filename(filepath.Base(w.Path))
			dst := filepath.Join(dir, enc.Dir, name)

			base := float64(done) / float64(total)
			span := 1 / float64(total)
			onProgress := func(f float64) {
				emit(Event{Stage: StageEncode, Detail: enc.ID + "/" + name, Fraction: base + f*span})
			}

			if err := p.Audio.Encode(ctx, w.Path, dst, enc, duration, onProgress); err != nil {
				return nil, err
			}

			o := Output{Kind: w.Kind, Instrument: w.Instrument, Encoding: enc.ID, Path: dst}
			if info, statErr := os.Stat(dst); statErr == nil {
				o.Bytes = info.Size()
			}
			out = append(out, o)
			done++
		}
	}
	return out, nil
}

// pathsExcluding returns every stem path except the named one.
func pathsExcluding(stems []separate.Stem, exclude string) []string {
	out := make([]string, 0, len(stems))
	for _, s := range stems {
		if s.Name != exclude {
			out = append(out, s.Path)
		}
	}
	return out
}

func stemNames(stems []separate.Stem) []string {
	out := make([]string, len(stems))
	for i, s := range stems {
		out[i] = s.Name
	}
	return out
}

func modelLabel(model string) string {
	if model == "" {
		return separate.ModelDefault
	}
	return model
}

// exportToUSB copies finished files to the root of a drive. The module only
// finds audio songs in the root directory, so no subfolder is created.
func exportToUSB(outputs []Output, dest string, onProgress func(float64)) ([]string, error) {
	info, err := os.Stat(dest)
	if err != nil {
		return nil, fmt.Errorf("USB destination %s: %w", dest, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("USB destination %s is not a directory", dest)
	}

	// The module plays WAV only, so the shareable copies stay off the drive.
	// Copying them would waste space and clutter the module's file list.
	var playable []Output
	for _, o := range outputs {
		if o.Encoding == audio.EncodingWAV {
			playable = append(playable, o)
		}
	}
	if len(playable) == 0 {
		return nil, nil
	}

	var warnings []string
	if existing, err := filepath.Glob(filepath.Join(dest, "*.wav")); err == nil {
		if total := len(existing) + len(playable); total > dtxspec.MaxWavFiles {
			warnings = append(warnings, fmt.Sprintf(
				"drive would hold %d .wav files; the module only lists %d",
				total, dtxspec.MaxWavFiles))
		}
	}

	for i, o := range playable {
		if err := copyFile(o.Path, filepath.Join(dest, o.Name())); err != nil {
			return warnings, err
		}
		if onProgress != nil {
			onProgress(float64(i+1) / float64(len(playable)))
		}
	}
	return warnings, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copying %s: %w", filepath.Base(src), err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s: %w", filepath.Base(src), err)
	}
	return out.Close()
}

// slug builds a filesystem-friendly folder name. The video ID is appended so
// two videos with the same title never collide, and so a folder can be traced
// back to its source.
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

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// ErrCancelled reports a run stopped by the user.
var ErrCancelled = errors.New("cancelled")
