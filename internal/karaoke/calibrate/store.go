package calibrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Key identifies what a calibration is valid for. The echo path depends on the
// speakers and mics (the device), on the rate the device runs at (the filter is
// measured in samples) and on which outputs feed the speakers.
type Key struct {
	Device     string
	SampleRate int
	Outputs    [2]int
}

// Key is the key this result is stored under.
func (r Result) Key() Key {
	return Key{Device: r.Device, SampleRate: r.SampleRate, Outputs: r.Outputs}
}

// filename is a readable, filesystem-safe name. Sanitizing is lossy (two device
// names can map to one file), so Load also compares the key stored inside the
// file rather than trusting the name.
func (k Key) filename() string {
	var b strings.Builder
	for _, r := range strings.ToLower(k.Device) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "device"
	}
	return fmt.Sprintf("%s_%d_%d-%d.json", name, k.SampleRate, k.Outputs[0], k.Outputs[1])
}

// Save writes the result atomically: a temporary file in the same directory is
// written and synced, then renamed over the target, so a crash leaves either
// the old calibration or the new one, never half of one.
func Save(dir string, r Result) error {
	data, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return fmt.Errorf("encode calibration: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create calibration dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".calibration-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary calibration file: %w", err)
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write calibration: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync calibration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close calibration: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, r.Key().filename())); err != nil {
		return fmt.Errorf("store calibration: %w", err)
	}
	return nil
}

// Load reads the calibration for key. A missing file, or one that belongs to a
// different key, is (zero, false, nil): the caller just has to calibrate. A
// file that exists but cannot be decoded is an error, so a corrupt store is
// noticed instead of silently ignored.
func Load(dir string, key Key) (Result, bool, error) {
	path := filepath.Join(dir, key.filename())
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("read calibration: %w", err)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, false, fmt.Errorf("decode calibration %s: %w", path, err)
	}
	if r.Key() != key {
		return Result{}, false, nil
	}
	return r, true, nil
}

// DefaultMaxAge is how long a calibration is trusted. Rooms and speaker
// placement drift; a month is long enough not to nag and short enough to catch
// a rearranged room.
const DefaultMaxAge = 30 * 24 * time.Hour

// StaleConfig says what a calibration has to satisfy to be reused.
type StaleConfig struct {
	// MaxAge 0 means DefaultMaxAge.
	MaxAge time.Duration
	// Inputs are the 1-based capture channels the session will use.
	Inputs []int
}

// Stale reports whether the session should calibrate again: the result is
// older than the limit, or some selected input was never calibrated. An input
// that was calibrated but flagged NoEchoPath counts as present: recalibrating
// cannot help until the user fixes the mic, and nagging every start would not.
func Stale(r Result, now time.Time, cfg StaleConfig) bool {
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = DefaultMaxAge
	}
	if now.Sub(r.Time) > maxAge {
		return true
	}
	for _, ch := range cfg.Inputs {
		found := false
		for _, in := range r.Inputs {
			if in.Channel == ch {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}
