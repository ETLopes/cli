package separate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardolopes/dtx/internal/runner"
	"github.com/eduardolopes/dtx/internal/runnertest"
)

// writeStems imitates what Demucs leaves on disk.
func writeStems(dir, model string, names []string) error {
	for _, n := range names {
		path := filepath.Join(dir, model, n+".wav")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("stem"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func fakeDemucs(names []string) *runnertest.Fake {
	f := runnertest.New()
	f.HandleFunc("demucs", nil, func(spec runner.Spec) runnertest.Response {
		out, _ := argAfter(spec.Args, "--out")
		model, _ := argAfter(spec.Args, "--name")
		return runnertest.Response{
			Stream: runner.Stderr,
			Lines:  []string{" 25%|## | ", "100%|####| "},
			Do:     func(runner.Spec) error { return writeStems(out, model, names) },
		}
	})
	return f
}

func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestSeparateReturnsSortedStems(t *testing.T) {
	dir := t.TempDir()
	f := fakeDemucs([]string{"vocals", "drums", "bass", "other"})

	stems, err := New(f).Separate(context.Background(), "in.m4a", dir, Options{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := make([]string, len(stems))
	for i, s := range stems {
		got[i] = s.Name
	}
	if strings.Join(got, ",") != "bass,drums,other,vocals" {
		t.Errorf("stems = %v, want them sorted by name", got)
	}
	for _, s := range stems {
		if _, err := os.Stat(s.Path); err != nil {
			t.Errorf("stem %s path is not readable: %v", s.Name, err)
		}
	}
}

// The stem set is discovered, not hardcoded, so a 6-stem model just works.
func TestSeparateDiscoversSixStems(t *testing.T) {
	dir := t.TempDir()
	names := []string{"bass", "drums", "guitar", "other", "piano", "vocals"}
	f := fakeDemucs(names)

	stems, err := New(f).Separate(context.Background(), "in.m4a", dir,
		Options{Model: ModelSixStem}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stems) != len(names) {
		t.Fatalf("got %d stems, want %d", len(stems), len(names))
	}
	if got, _ := f.CallsTo("demucs")[0].ArgAfter("--name"); got != ModelSixStem {
		t.Errorf("--name = %q, want %q", got, ModelSixStem)
	}
}

func TestSeparateFlattensOutputNames(t *testing.T) {
	dir := t.TempDir()
	f := fakeDemucs([]string{"drums"})

	if _, err := New(f).Separate(context.Background(), "in.m4a", dir, Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Demucs's default layout nests per-track folders; a flat name keeps the
	// results predictable.
	if got, _ := f.CallsTo("demucs")[0].ArgAfter("--filename"); got != "{stem}.{ext}" {
		t.Errorf("--filename = %q, want {stem}.{ext}", got)
	}
}

// Separation is the expensive stage; existing results must be reused.
func TestSeparateReusesExistingStems(t *testing.T) {
	dir := t.TempDir()
	if err := writeStems(dir, ModelDefault, []string{"bass", "drums", "other", "vocals"}); err != nil {
		t.Fatal(err)
	}

	f := runnertest.New()
	stems, err := New(f).Separate(context.Background(), "in.m4a", dir, Options{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stems) != 4 {
		t.Errorf("got %d stems, want 4", len(stems))
	}
	if got := len(f.CallsTo("demucs")); got != 0 {
		t.Errorf("ran demucs %d times, want 0 when stems already exist", got)
	}
}

func TestSeparatePassesTuningOptions(t *testing.T) {
	dir := t.TempDir()
	f := fakeDemucs([]string{"drums"})

	_, err := New(f).Separate(context.Background(), "in.m4a", dir,
		Options{Shifts: 3, Jobs: 4, Device: DeviceCPU}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := f.CallsTo("demucs")[0]
	for flag, want := range map[string]string{"--shifts": "3", "--jobs": "4", "--device": "cpu"} {
		if got, _ := c.ArgAfter(flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
}

// GPU backends fail on some model/OS combinations; falling back beats making
// the user work out what "mps" means and rerun.
func TestSeparateFallsBackToCPUWhenGPUFails(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.HandleFunc("demucs", nil, func(spec runner.Spec) runnertest.Response {
		device, _ := argAfter(spec.Args, "--device")
		if device != DeviceCPU {
			return runnertest.Response{Err: &runner.ExitError{
				Command: "demucs", ExitCode: 1,
				Output: []string{"NotImplementedError: aten::_fft_r2c not supported on MPS"},
			}}
		}
		out, _ := argAfter(spec.Args, "--out")
		model, _ := argAfter(spec.Args, "--name")
		return runnertest.Response{Do: func(runner.Spec) error {
			return writeStems(out, model, []string{"bass", "drums", "other", "vocals"})
		}}
	})

	stems, err := New(f).Separate(context.Background(), "in.m4a", dir,
		Options{Device: DeviceMPS}, nil)
	if err != nil {
		t.Fatalf("expected a CPU fallback to succeed, got: %v", err)
	}
	if len(stems) != 4 {
		t.Errorf("got %d stems, want 4", len(stems))
	}
	if got := len(f.CallsTo("demucs")); got != 2 {
		t.Errorf("made %d demucs calls, want 2 (gpu attempt then cpu retry)", got)
	}
}

// When a device error triggers a CPU retry and that fails too, both failures
// must be reported -- the user needs to know the fallback was already tried.
func TestSeparateReportsBothFailures(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.Handle("demucs", nil, runnertest.Response{
		Err: &runner.ExitError{
			Command: "demucs", ExitCode: 1,
			Output: []string{"NotImplementedError: aten::_fft_r2c not supported on MPS"},
		},
	})

	_, err := New(f).Separate(context.Background(), "in.m4a", dir, Options{Device: DeviceMPS}, nil)
	if err == nil {
		t.Fatal("expected an error when every attempt fails")
	}
	if !strings.Contains(err.Error(), "CPU") {
		t.Errorf("error should mention the CPU retry, got: %v", err)
	}
}

func TestSeparateFailsWhenNoStemsWritten(t *testing.T) {
	f := runnertest.New()
	f.Handle("demucs", nil, runnertest.Response{})

	_, err := New(f).Separate(context.Background(), "in.m4a", t.TempDir(), Options{}, nil)
	if err == nil {
		t.Fatal("expected an error when demucs writes no stems")
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		line string
		want float64
		ok   bool
	}{
		{" 45%|####      | 45/100", 0.45, true},
		{"100%|##########| 100/100", 1, true},
		{"Separating track", 0, false},
		{"no percent here", 0, false},
	}
	for _, tt := range tests {
		got, ok := parsePercent(tt.line)
		if ok != tt.ok {
			t.Errorf("parsePercent(%q) matched = %v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("parsePercent(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// Retrying a broken Python environment on CPU cannot help: it fails the same
// way, doubling both the wait and the error text the user has to read.
func TestSeparateDoesNotRetryNonDeviceFailures(t *testing.T) {
	f := runnertest.New()
	f.Handle("demucs", nil, runnertest.Response{
		Err: &runner.ExitError{
			Command: "demucs", ExitCode: 1,
			Output: []string{"ModuleNotFoundError: No module named 'numpy'"},
		},
	})

	_, err := New(f).Separate(context.Background(), "in.m4a", t.TempDir(),
		Options{Device: DeviceMPS}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := len(f.CallsTo("demucs")); got != 1 {
		t.Errorf("made %d demucs calls, want 1 (an import error must not be retried)", got)
	}
}

func TestIsDeviceError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"mps unsupported op", "NotImplementedError: aten::_fft_r2c not supported on MPS", true},
		{"cuda oom", "CUDA out of memory", true},
		{"missing numpy", "ModuleNotFoundError: No module named 'numpy'", false},
		{"import error", "ImportError: cannot import name x", false},
		{"missing model", "checkpoint not found", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeviceError(errors.New(tt.msg)); got != tt.want {
				t.Errorf("isDeviceError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
	if isDeviceError(nil) {
		t.Error("nil is not a device error")
	}
}
