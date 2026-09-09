package audio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardolopes/dtx/internal/dtxspec"
	"github.com/eduardolopes/dtx/internal/runner"
	"github.com/eduardolopes/dtx/internal/runnertest"
)

func probeJSON(codec string, rate, channels int, duration string) string {
	return `{"streams":[{"codec_name":"` + codec + `","sample_rate":"` + itoa(rate) +
		`","channels":` + itoa(channels) + `}],"format":{"duration":"` + duration + `"}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestProbeParsesFormat(t *testing.T) {
	f := runnertest.New()
	f.Handle("ffprobe", nil, runnertest.Response{Stdout: probeJSON("aac", 48000, 1, "212.5")})

	got, err := New(f).Probe(context.Background(), "in.m4a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Codec != "aac" || got.SampleRate != 48000 || got.Channels != 1 {
		t.Errorf("got %+v, want aac/48000/1", got)
	}
	if want := 212500 * time.Millisecond; got.Duration != want {
		t.Errorf("Duration = %v, want %v", got.Duration, want)
	}
}

func TestProbeRejectsFileWithNoAudio(t *testing.T) {
	f := runnertest.New()
	f.Handle("ffprobe", nil, runnertest.Response{Stdout: `{"streams":[],"format":{}}`})

	if _, err := New(f).Probe(context.Background(), "video.mp4"); err == nil {
		t.Fatal("expected an error when the file has no audio stream")
	} else if !strings.Contains(err.Error(), "no audio stream") {
		t.Errorf("error = %v, want it to mention a missing audio stream", err)
	}
}

func TestToDTXRequestsModuleFormat(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.Handle("ffmpeg", nil, runnertest.Response{})

	dst := filepath.Join(dir, "out.wav")
	if err := New(f).ToDTX(context.Background(), "in.m4a", dst, time.Minute, Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := f.CallsTo("ffmpeg")
	if len(calls) != 1 {
		t.Fatalf("got %d ffmpeg calls, want 1", len(calls))
	}
	c := calls[0]
	for flag, want := range map[string]string{
		"-ar": "44100", "-ac": "2", "-c:a": dtxspec.Codec, "-f": dtxspec.Container,
	} {
		if got, ok := c.ArgAfter(flag); !ok || got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	// Video streams would otherwise be carried into a WAV container.
	if !c.HasArg("-vn") {
		t.Error("expected -vn to drop any video stream")
	}
	if !c.HasArg("-nostdin") {
		t.Error("expected -nostdin so ffmpeg never competes for the terminal")
	}
}

func TestMixSumsWithoutAttenuation(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.Handle("ffmpeg", nil, runnertest.Response{})

	srcs := []string{"a.wav", "b.wav", "c.wav"}
	dst := filepath.Join(dir, "mix.wav")
	if err := New(f).Mix(context.Background(), srcs, dst, time.Minute, Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := f.CallsTo("ffmpeg")[0]
	chain, ok := c.ArgAfter("-filter_complex")
	if !ok {
		t.Fatal("expected a -filter_complex argument")
	}
	if !strings.Contains(chain, "amix=inputs=3") {
		t.Errorf("filter chain = %q, want it to mix 3 inputs", chain)
	}
	// Without normalize=0, amix divides by the input count and the mix comes
	// out several dB quieter than the source recording.
	if !strings.Contains(chain, "normalize=0") {
		t.Errorf("filter chain = %q, want normalize=0", chain)
	}
	for _, s := range srcs {
		if !c.HasArg(s) {
			t.Errorf("expected %s to be passed as an input", s)
		}
	}
}

func TestMixWithSingleInputSkipsTheMixer(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.Handle("ffmpeg", nil, runnertest.Response{})

	if err := New(f).Mix(context.Background(), []string{"only.wav"},
		filepath.Join(dir, "out.wav"), 0, Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c := f.CallsTo("ffmpeg")[0]; c.HasArg("-filter_complex") {
		t.Error("a single input needs no mixer")
	}
}

func TestOptionsBuildFilterChain(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"none", Options{}, nil},
		{"normalize defaults to -14 LUFS", Options{Normalize: true}, []string{"loudnorm=I=-14"}},
		{"explicit target", Options{Normalize: true, LoudnessTarget: -9}, []string{"loudnorm=I=-9"}},
		{"limiter", Options{Limit: true}, []string{"alimiter"}},
		{"both", Options{Normalize: true, Limit: true}, []string{"loudnorm", "alimiter"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(tt.opts.filters(), ",")
			if len(tt.want) == 0 {
				if got != "" {
					t.Errorf("filters = %q, want empty", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("filters = %q, want it to contain %q", got, w)
				}
			}
		})
	}
}

func TestEnsureDTXCopiesConformingInput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "drums.wav")
	if err := os.WriteFile(src, []byte("audio-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := runnertest.New()
	f.Handle("ffprobe", nil, runnertest.Response{
		Stdout: probeJSON(dtxspec.Codec, dtxspec.SampleRate, dtxspec.Channels, "10.0"),
	})
	f.Handle("ffmpeg", nil, runnertest.Response{})

	dst := filepath.Join(dir, "out", "Drums.wav")
	if err := New(f).EnsureDTX(context.Background(), src, dst, Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(f.CallsTo("ffmpeg")); got != 0 {
		t.Errorf("re-encoded a conforming file %d times", got)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading destination: %v", err)
	}
	if string(body) != "audio-bytes" {
		t.Errorf("copied content = %q, want the original bytes", body)
	}
}

func TestEnsureDTXConvertsNonConformingInput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "stem.wav")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := runnertest.New()
	// A 48 kHz stem would silently fail to play on the module.
	f.Handle("ffprobe", nil, runnertest.Response{Stdout: probeJSON(dtxspec.Codec, 48000, 2, "10.0")})
	f.Handle("ffmpeg", nil, runnertest.Response{})

	if err := New(f).EnsureDTX(context.Background(), src,
		filepath.Join(dir, "out.wav"), Options{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(f.CallsTo("ffmpeg")); got != 1 {
		t.Errorf("got %d ffmpeg calls, want 1 for a non-conforming file", got)
	}
}

func TestProgressReportedFromFFmpegOutput(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.Handle("ffmpeg", nil, runnertest.Response{
		Lines: []string{"out_time_ms=30000000", "out_time_ms=60000000"},
	})

	var seen []float64
	err := New(f).ToDTX(context.Background(), "in.wav", filepath.Join(dir, "o.wav"),
		2*time.Minute, Options{}, func(fr float64) { seen = append(seen, fr) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) < 3 {
		t.Fatalf("got %d progress updates %v, want at least 3", len(seen), seen)
	}
	if seen[0] != 0.25 {
		t.Errorf("first fraction = %v, want 0.25 (30s of 120s)", seen[0])
	}
	if last := seen[len(seen)-1]; last != 1 {
		t.Errorf("final fraction = %v, want 1", last)
	}
}

func TestParseProgressTime(t *testing.T) {
	// ffmpeg names the field "ms" but reports microseconds.
	if d, ok := parseProgressTime("out_time_ms=1500000"); !ok || d != 1500*time.Millisecond {
		t.Errorf("got %v (%v), want 1.5s", d, ok)
	}
	for _, line := range []string{"frame=12", "out_time_ms=", "out_time_ms=abc", ""} {
		if _, ok := parseProgressTime(line); ok {
			t.Errorf("parseProgressTime(%q) should not match", line)
		}
	}
}

func TestFFmpegErrorNamesTheStep(t *testing.T) {
	f := runnertest.New()
	f.Handle("ffmpeg", nil, runnertest.Response{
		Err: &runner.ExitError{Command: "ffmpeg", ExitCode: 1, Output: []string{"Invalid data"}},
	})
	err := New(f).ToDTX(context.Background(), "broken.m4a", filepath.Join(t.TempDir(), "o.wav"), 0, Options{}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "broken.m4a") {
		t.Errorf("error should name the input file, got: %v", err)
	}
}
