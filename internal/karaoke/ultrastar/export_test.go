package ultrastar

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/karaoke/song"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
)

// fixtureSong writes a prepared song directory with a C4/E4/G4 contour.
func fixtureSong(t *testing.T, m song.Manifest, lrc string) song.Song {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("source.m4a", "audio")
	write("instrumental.wav", "audio")
	write("stems/2stems-vocals/m/vocals.wav", "audio")
	write("stems/2stems-vocals/m/no_vocals.wav", "audio")

	c := &reference.Contour{HopSeconds: 0.01, SampleRate: 16000}
	for i := 0; i < 90; i++ {
		midi := []float64{60, 64, 67}[i/30]
		c.Time = append(c.Time, float64(i)*0.01)
		c.Hz = append(c.Hz, 440*math.Pow(2, (midi-69)/12))
		c.Confidence = append(c.Confidence, 1)
		c.LoudnessDB = append(c.LoudnessDB, -20)
	}
	if err := c.Save(filepath.Join(dir, "reference.json")); err != nil {
		t.Fatal(err)
	}

	m.Version = song.ManifestVersion
	m.DurationMS = 900
	m.Stages = map[song.Stage]song.StageRecord{
		song.StageDownload: {Artifacts: []string{"source.m4a"}},
		song.StageSeparate: {Artifacts: []string{"stems/2stems-vocals/m/vocals.wav", "stems/2stems-vocals/m/no_vocals.wav"}},
	}
	if lrc != "" {
		write("lyrics.lrc", lrc)
		m.Lyrics = song.LyricsSynced
	} else {
		m.Lyrics = song.LyricsNone
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write("song.json", string(data))

	s, err := song.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fakeFFmpeg() *runnertest.Fake {
	f := runnertest.New()
	f.HandleFunc("ffmpeg", nil, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(spec runner.Spec) error {
			return os.WriteFile(spec.Args[len(spec.Args)-1], []byte("RIFF"), 0o644)
		}}
	})
	return f
}

func TestExportWritesChartAndThreeWAVsWithMatchingHeaders(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Title: "Video Title", Artist: "The Band", Track: "Placeholder Tune"},
		"[00:00.00] one two three\n[00:01.00]\n")
	fake := fakeFFmpeg()
	out := t.TempDir()

	res, err := Export(context.Background(), s, out, audio.New(fake))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}
	wantDir := filepath.Join(out, "The Band - Placeholder Tune")
	if res.Dir != wantDir {
		t.Errorf("dir = %q, want %q", res.Dir, wantDir)
	}

	calls := fake.CallsTo("ffmpeg")
	if len(calls) != 3 {
		t.Fatalf("ffmpeg calls = %d, want 3", len(calls))
	}
	wantSrc := []string{"source.m4a", "instrumental.wav", "vocals.wav"}
	for i, c := range calls {
		for flag, want := range map[string]string{"-ar": "44100", "-ac": "2", "-c:a": "pcm_s16le", "-f": "wav"} {
			if got, _ := c.ArgAfter(flag); got != want {
				t.Errorf("call %d %s = %q, want %q", i, flag, got, want)
			}
		}
		in, _ := c.ArgAfter("-i")
		if !strings.HasSuffix(in, wantSrc[i]) {
			t.Errorf("call %d input = %q, want suffix %q", i, in, wantSrc[i])
		}
	}

	data, err := os.ReadFile(res.Chart)
	if err != nil {
		t.Fatal(err)
	}
	p := parseChart(t, data)
	for key, want := range map[string]string{
		"AUDIO":        "The Band - Placeholder Tune.wav",
		"INSTRUMENTAL": "The Band - Placeholder Tune [INSTR].wav",
		"VOCALS":       "The Band - Placeholder Tune [VOC].wav",
		"TITLE":        "Placeholder Tune",
		"ARTIST":       "The Band",
	} {
		if p.headers[key] != want {
			t.Errorf("%s = %q, want %q", key, p.headers[key], want)
		}
		if key == "AUDIO" || key == "INSTRUMENTAL" || key == "VOCALS" {
			if info, err := os.Stat(filepath.Join(res.Dir, p.headers[key])); err != nil || info.Size() == 0 {
				t.Errorf("%s file %q not written: %v", key, p.headers[key], err)
			}
		}
	}
	if len(p.notes) != 3 || p.notes[0].Text != "one " || p.notes[1].Text != "two " || p.notes[2].Text != "three" {
		t.Errorf("notes = %+v", p.notes)
	}
	if p.notes[1].Pitch != 4 || p.notes[2].Pitch != 7 {
		t.Errorf("pitches = %+v", p.notes)
	}
}

func TestExportWithoutLyricsUsesContinuationAndWarns(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Title: "Only Title", Artist: "A"}, "")
	res, err := Export(context.Background(), s, t.TempDir(), audio.New(fakeFFmpeg()))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != WarningNoLyrics {
		t.Errorf("warnings = %v", res.Warnings)
	}
	data, _ := os.ReadFile(res.Chart)
	p := parseChart(t, data)
	if p.headers["TITLE"] != "Only Title" {
		t.Errorf("title fell back to %q", p.headers["TITLE"])
	}
	for _, n := range p.notes {
		if n.Text != "~" {
			t.Errorf("text = %q", n.Text)
		}
	}
}

func TestExportSanitizesFolderAndFileNames(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Artist: "AC/DC", Track: `a/b:c*d?e"f<g>h|i`}, "")
	res, err := Export(context.Background(), s, t.TempDir(), audio.New(fakeFFmpeg()))
	if err != nil {
		t.Fatal(err)
	}
	const want = "AC_DC - a_b_c_d_e_f_g_h_i"
	if filepath.Base(res.Dir) != want {
		t.Errorf("folder = %q, want %q", filepath.Base(res.Dir), want)
	}
	if filepath.Base(res.Chart) != want+".txt" {
		t.Errorf("file = %q", filepath.Base(res.Chart))
	}
	data, _ := os.ReadFile(res.Chart)
	if p := parseChart(t, data); p.headers["TITLE"] != `a/b:c*d?e"f<g>h|i` {
		t.Errorf("TITLE header should keep the real title, got %q", p.headers["TITLE"])
	}
}

func TestSanitizeNameTrimsAndHandlesEmpty(t *testing.T) {
	for in, want := range map[string]string{
		"  spaced   out  ": "spaced out",
		"trailing dot...":  "trailing dot",
		"...":              "song",
		"":                 "song",
		"tab\tnew\nline":   "tab_new_line",
	} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportIsIdempotentAndLeavesNoTempFiles(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Artist: "A", Track: "T"}, "[00:00.00] la la la\n")
	out := t.TempDir()
	conv := audio.New(fakeFFmpeg())
	first, err := Export(context.Background(), s, out, conv)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first.Chart)
	second, err := Export(context.Background(), s, out, conv)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(second.Chart)
	if string(a) != string(b) {
		t.Error("rerun changed the chart")
	}
	entries, _ := os.ReadDir(first.Dir)
	if len(entries) != 4 {
		t.Errorf("folder has %d entries, want 4 (txt + 3 wav)", len(entries))
	}
}

func TestExportFailsBeforeRenderingWhenThereAreNoNotes(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Artist: "A", Track: "T"}, "")
	silent := &reference.Contour{HopSeconds: 0.01, Time: []float64{0, 0.01}, Hz: []float64{0, 0}, Confidence: []float64{0, 0}, LoudnessDB: []float64{-90, -90}}
	if err := silent.Save(s.ReferencePath()); err != nil {
		t.Fatal(err)
	}
	fake := fakeFFmpeg()
	if _, err := Export(context.Background(), s, t.TempDir(), audio.New(fake)); err == nil {
		t.Fatal("expected an error")
	}
	if n := len(fake.CallsTo("ffmpeg")); n != 0 {
		t.Errorf("ffmpeg ran %d times before the failure", n)
	}
}

func TestExportReportsMissingVocalStem(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Artist: "A", Track: "T"}, "")
	s.Stages[song.StageSeparate] = song.StageRecord{}
	if _, err := Export(context.Background(), s, t.TempDir(), audio.New(fakeFFmpeg())); err == nil {
		t.Fatal("expected an error")
	}
}

func TestExportStopsOnCancelledContext(t *testing.T) {
	s := fixtureSong(t, song.Manifest{Artist: "A", Track: "T"}, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Export(ctx, s, t.TempDir(), audio.New(fakeFFmpeg())); err == nil {
		t.Fatal("expected an error")
	}
}
