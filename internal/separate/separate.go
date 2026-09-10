// Package separate drives Demucs to split a mixed recording into instrument
// stems. It treats the stem set as something to discover rather than assume,
// so switching from the 4-stem default to a 6-stem model needs no code change.
package separate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/ETLopes/cli/internal/runner"
)

// Tool is the executable this package drives.
const Tool = "demucs"

// Models known to be worth offering. Demucs accepts others; these are the ones
// the CLI advertises.
const (
	// ModelDefault is the standard 4-stem Hybrid Transformer model:
	// drums, bass, vocals, other.
	ModelDefault = "htdemucs"
	// ModelFineTuned is the fine-tuned variant of the default. Noticeably
	// cleaner separation for roughly four times the processing time.
	ModelFineTuned = "htdemucs_ft"
	// ModelSixStem additionally splits out guitar and piano. The extra stems
	// are experimental and weaker than the core four, but they yield more
	// minus-one mixes.
	ModelSixStem = "htdemucs_6s"
)

// Device selects the compute backend Demucs runs on.
const (
	DeviceAuto = "auto"
	DeviceCPU  = "cpu"
	DeviceMPS  = "mps"  // Apple Silicon GPU
	DeviceCUDA = "cuda" // NVIDIA GPU
)

// Separator splits recordings into stems.
type Separator struct {
	Run runner.Runner
	// Path overrides the demucs executable. Empty resolves "demucs" on PATH.
	Path string
}

// New returns a Separator backed by r.
func New(r runner.Runner) *Separator { return &Separator{Run: r} }

func (s *Separator) bin() string {
	if s.Path != "" {
		return s.Path
	}
	return Tool
}

// Options tune a separation run.
type Options struct {
	// Model is the Demucs model name. Empty means ModelDefault.
	Model string
	// Device is the compute backend. Empty or DeviceAuto picks the best
	// available for the host.
	Device string
	// Shifts enables shift trick averaging: higher values improve quality at
	// a directly proportional cost in time. Zero disables it.
	Shifts int
	// Jobs is the number of parallel worker processes. Zero lets Demucs decide.
	Jobs int
}

// Stem is one separated instrument track.
type Stem struct {
	// Name is the instrument, e.g. "drums", lowercase as Demucs names it.
	Name string
	// Path is the location of the stem's WAV file on disk.
	Path string
}

// ProgressFunc reports fractional separation completion in [0,1].
type ProgressFunc func(fraction float64)

// Separate splits src into stems written under outDir, returning them sorted by
// name. Demucs is asked to write flat filenames ("drums.wav") rather than its
// default nested per-track layout, so the results are easy to locate.
//
// If a previous run already produced stems in outDir they are reused, which
// matters because separation is by far the most expensive stage.
func (s *Separator) Separate(ctx context.Context, src, outDir string, opts Options, onProgress ProgressFunc) ([]Stem, error) {
	model := opts.Model
	if model == "" {
		model = ModelDefault
	}
	// Demucs always nests output one level under the model name.
	stemDir := filepath.Join(outDir, model)

	if stems, err := discover(stemDir); err == nil && len(stems) > 0 {
		if onProgress != nil {
			onProgress(1)
		}
		return stems, nil
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating stem directory: %w", err)
	}

	device := opts.Device
	if device == "" || device == DeviceAuto {
		device = autoDevice()
	}

	err := s.run(ctx, src, outDir, model, device, opts, onProgress)
	if err != nil && device != DeviceCPU && !isCancelled(ctx) && isDeviceError(err) {
		// GPU backends -- MPS especially -- fail on some model/OS combinations
		// with an unsupported-operation error. Falling back to CPU is slower
		// but reliable, and is far friendlier than making the user rerun.
		// Anything else (a broken install, a missing model) fails identically
		// on CPU, so retrying would only double the wait and the error text.
		if fallbackErr := s.run(ctx, src, outDir, model, DeviceCPU, opts, onProgress); fallbackErr == nil {
			err = nil
		} else {
			err = fmt.Errorf("%w (retry on CPU also failed: %v)", err, fallbackErr)
		}
	}
	if err != nil {
		return nil, err
	}

	stems, discErr := discover(stemDir)
	if discErr != nil {
		return nil, discErr
	}
	if len(stems) == 0 {
		return nil, fmt.Errorf("separating audio: demucs reported success but wrote no stems to %s", stemDir)
	}
	if onProgress != nil {
		onProgress(1)
	}
	return stems, nil
}

func (s *Separator) run(ctx context.Context, src, outDir, model, device string, opts Options, onProgress ProgressFunc) error {
	args := []string{
		"--name", model,
		"--out", outDir,
		"--filename", "{stem}.{ext}",
		"--device", device,
	}
	if opts.Shifts > 0 {
		args = append(args, "--shifts", strconv.Itoa(opts.Shifts))
	}
	if opts.Jobs > 0 {
		args = append(args, "--jobs", strconv.Itoa(opts.Jobs))
	}
	args = append(args, src)

	var onLine runner.LineFunc
	if onProgress != nil {
		onLine = func(_ runner.Stream, line string) {
			if f, ok := parsePercent(line); ok {
				onProgress(f)
			}
		}
	}

	if _, err := s.Run.Run(ctx, runner.Spec{Name: s.bin(), Args: args, OnLine: onLine}); err != nil {
		return fmt.Errorf("separating audio: %w", err)
	}
	return nil
}

// autoDevice picks the fastest backend likely to work on this host. Apple
// Silicon exposes the GPU through Metal Performance Shaders, which is several
// times faster than CPU for these models.
func autoDevice() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return DeviceMPS
	}
	return DeviceCPU
}

func isCancelled(ctx context.Context) bool { return ctx.Err() != nil }

// deviceErrorSignals are phrases that indicate a failure specific to the
// compute backend, and so worth retrying on CPU.
var deviceErrorSignals = []string{
	"mps", "cuda", "device", "notimplementederror", "not implemented for",
	"out of memory", "backend", "gpu",
}

// isDeviceError reports whether err looks like a GPU-backend problem rather
// than a failure that would recur identically on CPU.
func isDeviceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// A broken Python environment fails the same way on every backend, so
	// never burn a second full run on it.
	for _, fatal := range []string{"modulenotfounderror", "importerror", "no module named"} {
		if strings.Contains(msg, fatal) {
			return false
		}
	}
	for _, sig := range deviceErrorSignals {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// discover lists the stem files Demucs wrote into dir.
func discover(dir string) ([]Stem, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.wav"))
	if err != nil {
		return nil, fmt.Errorf("locating stems: %w", err)
	}
	stems := make([]Stem, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || info.Size() == 0 {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(m), filepath.Ext(m))
		stems = append(stems, Stem{Name: name, Path: m})
	}
	sort.Slice(stems, func(i, j int) bool { return stems[i].Name < stems[j].Name })
	return stems, nil
}

// percentPattern matches the tqdm progress bar Demucs writes to stderr, which
// looks like " 45%|####      | 45/100 [00:10<00:12]".
var percentPattern = regexp.MustCompile(`(\d{1,3})%\|`)

func parsePercent(line string) (float64, bool) {
	m := percentPattern.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return min(float64(v)/100, 1), true
}
