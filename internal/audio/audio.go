// Package audio wraps ffmpeg and ffprobe to inspect audio files and render them
// into the format the DTX-PRO requires. Callers describe what they want in
// terms of the module ("make this DTX-ready", "mix these stems"), never in
// terms of ffmpeg flags.
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eduardolopes/dtx/internal/dtxspec"
	"github.com/eduardolopes/dtx/internal/runner"
)

// Tool names, resolved on PATH.
const (
	FFmpeg  = "ffmpeg"
	FFprobe = "ffprobe"
)

// Converter renders audio into DTX-PRO format.
type Converter struct {
	Run runner.Runner
	// FFmpegPath and FFprobePath override the executables used. Empty values
	// fall back to resolving "ffmpeg"/"ffprobe" on PATH.
	FFmpegPath  string
	FFprobePath string
}

// New returns a Converter backed by r.
func New(r runner.Runner) *Converter { return &Converter{Run: r} }

func (c *Converter) ffmpeg() string {
	if c.FFmpegPath != "" {
		return c.FFmpegPath
	}
	return FFmpeg
}

func (c *Converter) ffprobe() string {
	if c.FFprobePath != "" {
		return c.FFprobePath
	}
	return FFprobe
}

// ProgressFunc reports fractional completion in [0,1]. Values are
// monotonically non-decreasing but may skip or repeat.
type ProgressFunc func(fraction float64)

// Probe reports the audio format of the first audio stream in path.
func (c *Converter) Probe(ctx context.Context, path string) (dtxspec.Format, error) {
	res, err := c.Run.Run(ctx, runner.Spec{
		Name: c.ffprobe(),
		Args: []string{
			"-v", "error",
			"-select_streams", "a:0",
			"-show_entries", "stream=codec_name,sample_rate,channels:format=duration",
			"-of", "json",
			path,
		},
	})
	if err != nil {
		return dtxspec.Format{}, fmt.Errorf("probe %s: %w", filepath.Base(path), err)
	}

	var parsed struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		return dtxspec.Format{}, fmt.Errorf("probe %s: parsing ffprobe output: %w", filepath.Base(path), err)
	}
	if len(parsed.Streams) == 0 {
		return dtxspec.Format{}, fmt.Errorf("probe %s: no audio stream found", filepath.Base(path))
	}

	s := parsed.Streams[0]
	f := dtxspec.Format{Codec: s.CodecName, Channels: s.Channels}
	f.SampleRate, _ = strconv.Atoi(s.SampleRate)
	if secs, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && secs > 0 {
		f.Duration = time.Duration(secs * float64(time.Second))
	}
	return f, nil
}

// Options tune how a rendered file is post-processed. The zero value performs a
// faithful conversion with no level changes.
type Options struct {
	// Normalize applies EBU R128 loudness normalization. Useful when a
	// minus-one mix feels quiet next to the full track, but it does alter the
	// dynamics of the recording.
	Normalize bool
	// LoudnessTarget is the integrated loudness in LUFS aimed for when
	// Normalize is set. Zero means -14 LUFS, a common streaming reference.
	LoudnessTarget float64
	// Limit applies a brickwall limiter just below full scale. Summing stems
	// can occasionally exceed 0 dBFS; this trades a touch of transient shape
	// for the certainty of no clipping on the way to 16-bit.
	Limit bool
}

const defaultLoudness = -14.0

// filters returns the post-processing filter chain, which may be empty.
func (o Options) filters() []string {
	var f []string
	if o.Normalize {
		target := o.LoudnessTarget
		if target == 0 {
			target = defaultLoudness
		}
		f = append(f, fmt.Sprintf("loudnorm=I=%g:TP=-1.5:LRA=11", target))
	}
	if o.Limit {
		f = append(f, "alimiter=limit=0.98")
	}
	return f
}

// dtxOutputArgs are the encoder flags that make ffmpeg emit exactly what the
// module expects.
func dtxOutputArgs() []string {
	return []string{
		"-ar", strconv.Itoa(dtxspec.SampleRate),
		"-ac", strconv.Itoa(dtxspec.Channels),
		"-c:a", dtxspec.Codec,
		"-f", dtxspec.Container,
	}
}

// baseArgs are flags common to every ffmpeg invocation: never prompt, never
// read stdin (which would fight with the TUI for the terminal), and report
// machine-readable progress on stdout instead of a redrawn stats line.
func baseArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-nostats"}
}

// ToDTX converts src into a DTX-PRO-ready WAV at dst, creating dst's directory
// if needed. total is the expected duration, used only to turn ffmpeg's
// progress output into a fraction; pass zero if unknown.
func (c *Converter) ToDTX(ctx context.Context, src, dst string, total time.Duration, opts Options, onProgress ProgressFunc) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	args := baseArgs()
	args = append(args, "-progress", "pipe:1", "-i", src, "-vn", "-map_metadata", "-1")
	if f := opts.filters(); len(f) > 0 {
		args = append(args, "-af", strings.Join(f, ","))
	}
	args = append(args, dtxOutputArgs()...)
	args = append(args, dst)

	return c.runFFmpeg(ctx, args, total, onProgress, "converting "+filepath.Base(src))
}

// Mix sums srcs into a single DTX-PRO-ready WAV at dst.
//
// The sum is deliberately un-normalized (amix normalize=0): Demucs stems add
// back up to the original recording, so scaling by 1/N -- amix's default --
// would silently drop every mix by several dB relative to the source track.
func (c *Converter) Mix(ctx context.Context, srcs []string, dst string, total time.Duration, opts Options, onProgress ProgressFunc) error {
	if len(srcs) == 0 {
		return fmt.Errorf("mix %s: no input files", filepath.Base(dst))
	}
	if len(srcs) == 1 {
		return c.ToDTX(ctx, srcs[0], dst, total, opts, onProgress)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	args := baseArgs()
	args = append(args, "-progress", "pipe:1")
	for _, s := range srcs {
		args = append(args, "-i", s)
	}

	var chain strings.Builder
	for i := range srcs {
		fmt.Fprintf(&chain, "[%d:a]", i)
	}
	fmt.Fprintf(&chain, "amix=inputs=%d:normalize=0", len(srcs))
	for _, f := range opts.filters() {
		chain.WriteString("," + f)
	}
	chain.WriteString("[mixed]")

	args = append(args, "-filter_complex", chain.String(), "-map", "[mixed]")
	args = append(args, dtxOutputArgs()...)
	args = append(args, dst)

	return c.runFFmpeg(ctx, args, total, onProgress, "mixing "+filepath.Base(dst))
}

// EnsureDTX places a DTX-ready copy of src at dst, converting only when
// necessary. Demucs already emits 44.1 kHz 16-bit stereo WAV, so its stems
// normally just need copying -- but the format is verified rather than assumed,
// so a change in Demucs defaults degrades to a re-encode instead of producing
// files the module silently refuses to play.
func (c *Converter) EnsureDTX(ctx context.Context, src, dst string, opts Options, onProgress ProgressFunc) error {
	// Any post-processing means the audio has to be re-rendered regardless.
	if len(opts.filters()) == 0 {
		format, err := c.Probe(ctx, src)
		if err == nil && format.Conforms() && strings.EqualFold(filepath.Ext(src), "."+dtxspec.Container) {
			if err := copyFile(src, dst); err != nil {
				return err
			}
			if onProgress != nil {
				onProgress(1)
			}
			return nil
		}
	}
	return c.ToDTX(ctx, src, dst, 0, opts, onProgress)
}

// runFFmpeg executes ffmpeg, translating its -progress stream into fractions.
func (c *Converter) runFFmpeg(ctx context.Context, args []string, total time.Duration, onProgress ProgressFunc, what string) error {
	var onLine runner.LineFunc
	if onProgress != nil && total > 0 {
		onLine = func(s runner.Stream, line string) {
			if s != runner.Stdout {
				return
			}
			if d, ok := parseProgressTime(line); ok {
				if f := float64(d) / float64(total); f >= 0 {
					onProgress(min(f, 1))
				}
			}
		}
	}

	_, err := c.Run.Run(ctx, runner.Spec{Name: c.ffmpeg(), Args: args, OnLine: onLine})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if onProgress != nil {
		onProgress(1)
	}
	return nil
}

// parseProgressTime reads the "out_time_ms=..." lines ffmpeg emits under
// -progress. Despite the name the value is microseconds.
func parseProgressTime(line string) (time.Duration, bool) {
	const key = "out_time_ms="
	if !strings.HasPrefix(line, key) {
		return 0, false
	}
	micros, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, key)), 10, 64)
	if err != nil || micros < 0 {
		return 0, false
	}
	return time.Duration(micros) * time.Microsecond, true
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copying %s: %w", filepath.Base(src), err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("copying to %s: %w", filepath.Base(dst), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s: %w", filepath.Base(src), err)
	}
	return out.Close()
}
