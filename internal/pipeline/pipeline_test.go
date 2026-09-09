package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardolopes/dtx/internal/audio"
	"github.com/eduardolopes/dtx/internal/dtxspec"
	"github.com/eduardolopes/dtx/internal/runner"
	"github.com/eduardolopes/dtx/internal/runnertest"
	"github.com/eduardolopes/dtx/internal/separate"
	"github.com/eduardolopes/dtx/internal/youtube"
)

const (
	testTitle = "Ação de Graças (Live)"
	testID    = "dQw4w9WgXcQ"
)

// defaultStems is what the standard 4-stem model produces.
var defaultStems = []string{"bass", "drums", "other", "vocals"}

// newFake wires a Fake runner that imitates every external tool: yt-dlp
// reports metadata and writes a source file, demucs writes stems, ffprobe
// answers about formats, and ffmpeg writes its output file.
func newFake(t *testing.T, stems []string) *runnertest.Fake {
	t.Helper()
	f := runnertest.New()

	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: `{"id":"` + testID + `","title":"` + testTitle + `","uploader":"Chan","duration":212,"webpage_url":"https://youtu.be/` + testID + `"}`,
	})

	f.HandleFunc("yt-dlp", []string{"--output"}, func(spec runner.Spec) runnertest.Response {
		out, _ := callArgAfter(spec.Args, "--output")
		// yt-dlp expands the extension template itself.
		path := strings.ReplaceAll(out, "%(ext)s", "m4a")
		return runnertest.Response{
			Lines:  []string{"DTXPROG   0.0%", "DTXPROG  50.0%", "DTXPROG 100.0%"},
			Stream: runner.Stderr,
			Do:     func(runner.Spec) error { return writeFile(path, "source-audio") },
		}
	})

	f.HandleFunc("ffprobe", nil, func(spec runner.Spec) runnertest.Response {
		path := spec.Args[len(spec.Args)-1]
		// Demucs stems are already module-compatible; the download is not.
		if strings.Contains(path, "stems") {
			return runnertest.Response{Stdout: probeJSON(dtxspec.Codec, dtxspec.SampleRate, 2, "212.0")}
		}
		return runnertest.Response{Stdout: probeJSON("aac", 44100, 2, "212.0")}
	})

	f.HandleFunc("demucs", nil, func(spec runner.Spec) runnertest.Response {
		out, _ := callArgAfter(spec.Args, "--out")
		model, _ := callArgAfter(spec.Args, "--name")
		return runnertest.Response{
			Lines:  []string{" 10%|# | 1/10", " 60%|##### | 6/10", "100%|##########| 10/10"},
			Stream: runner.Stderr,
			Do: func(runner.Spec) error {
				for _, s := range stems {
					if err := writeFile(filepath.Join(out, model, s+".wav"), "stem-"+s); err != nil {
						return err
					}
				}
				return nil
			},
		}
	})

	f.HandleFunc("ffmpeg", nil, func(spec runner.Spec) runnertest.Response {
		dst := spec.Args[len(spec.Args)-1]
		return runnertest.Response{
			Lines: []string{"out_time_ms=106000000", "out_time_ms=212000000"},
			Do:    func(runner.Spec) error { return writeFile(dst, "rendered:"+filepath.Base(dst)) },
		}
	})

	return f
}

func newPipeline(f *runnertest.Fake) *Pipeline {
	return &Pipeline{
		YouTube:   youtube.New(f),
		Audio:     audio.New(f),
		Separator: separate.New(f),
	}
}

func runPipeline(t *testing.T, f *runnertest.Fake, req Request) *Result {
	t.Helper()
	if req.URL == "" {
		req.URL = "https://youtu.be/" + testID
	}
	res, err := newPipeline(f).Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	return res
}

func TestRunProducesEveryExpectedOutput(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{OutputDir: dir})

	// One full mix, one minus-one per stem, and each stem on its own.
	wantCount := 1 + len(defaultStems)*2
	if len(res.Outputs) != wantCount {
		t.Fatalf("got %d outputs, want %d: %v", len(res.Outputs), wantCount, names(res.Outputs))
	}

	byKind := map[Kind]int{}
	for _, o := range res.Outputs {
		byKind[o.Kind]++
		if _, err := os.Stat(o.Path); err != nil {
			t.Errorf("output %s was not written: %v", o.Name(), err)
		}
	}
	if byKind[KindFull] != 1 {
		t.Errorf("got %d full mixes, want 1", byKind[KindFull])
	}
	if byKind[KindMinusOne] != len(defaultStems) {
		t.Errorf("got %d minus-one mixes, want %d", byKind[KindMinusOne], len(defaultStems))
	}
	if byKind[KindStem] != len(defaultStems) {
		t.Errorf("got %d isolated stems, want %d", byKind[KindStem], len(defaultStems))
	}

	// Names are sanitized for the module: accents folded, no punctuation.
	for _, o := range res.Outputs {
		stem := strings.TrimSuffix(o.Name(), ".wav")
		for _, r := range stem {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !isAlnum {
				t.Errorf("output name %q contains non-alphanumeric %q", o.Name(), r)
			}
		}
	}
	if got := names(res.Outputs); !containsString(got, "AcaoDeGracasLiveNoDrums.wav") {
		t.Errorf("expected a NoDrums mix with a folded title, got %v", got)
	}
}

// The minus-one mix is the point of the tool: it must contain every stem
// except the excluded one, summed without amix's default attenuation.
func TestMinusOneMixExcludesOnlyItsOwnStem(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	runPipeline(t, f, Request{OutputDir: dir})

	for _, excluded := range defaultStems {
		wantOut := "No" + dtxspec.SanitizeName(excluded) + ".wav"
		var found *runnertest.Call
		for i, c := range f.CallsTo("ffmpeg") {
			if strings.HasSuffix(c.Args[len(c.Args)-1], wantOut) {
				found = &f.CallsTo("ffmpeg")[i]
				break
			}
		}
		if found == nil {
			t.Errorf("no ffmpeg call produced %s", wantOut)
			continue
		}

		inputs := inputPaths(*found)
		if len(inputs) != len(defaultStems)-1 {
			t.Errorf("%s: got %d inputs, want %d: %v", wantOut, len(inputs), len(defaultStems)-1, inputs)
		}
		for _, in := range inputs {
			if filepath.Base(in) == excluded+".wav" {
				t.Errorf("%s: mix wrongly includes the excluded stem %s", wantOut, excluded)
			}
		}
		if !found.HasArg("-filter_complex") {
			t.Errorf("%s: expected a filter_complex mix", wantOut)
		}
		joined := strings.Join(found.Args, " ")
		if !strings.Contains(joined, "normalize=0") {
			t.Errorf("%s: amix must use normalize=0 or the mix is quieter than the source", wantOut)
		}
	}
}

// Every file handed to the module must carry the exact format it requires.
func TestRenderedFilesTargetTheModuleFormat(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	runPipeline(t, f, Request{OutputDir: dir})

	calls := f.CallsTo("ffmpeg")
	if len(calls) == 0 {
		t.Fatal("expected ffmpeg to be invoked")
	}
	for _, c := range calls {
		if rate, ok := c.ArgAfter("-ar"); !ok || rate != "44100" {
			t.Errorf("%s: sample rate = %q, want 44100", filepath.Base(lastArg(c)), rate)
		}
		if ch, ok := c.ArgAfter("-ac"); !ok || ch != "2" {
			t.Errorf("%s: channels = %q, want 2", filepath.Base(lastArg(c)), ch)
		}
		if codec, ok := c.ArgAfter("-c:a"); !ok || codec != dtxspec.Codec {
			t.Errorf("%s: codec = %q, want %s", filepath.Base(lastArg(c)), codec, dtxspec.Codec)
		}
	}
}

// Demucs stems already conform, so re-encoding them would be wasted work.
func TestConformingStemsAreCopiedNotReEncoded(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{OutputDir: dir})

	rendered := map[string]bool{}
	for _, c := range f.CallsTo("ffmpeg") {
		rendered[filepath.Base(lastArg(c))] = true
	}
	for _, o := range res.Outputs {
		if o.Kind != KindStem {
			continue
		}
		if rendered[o.Name()] {
			t.Errorf("stem %s was re-encoded despite already conforming", o.Name())
		}
		body, err := os.ReadFile(o.Path)
		if err != nil {
			t.Fatalf("reading %s: %v", o.Name(), err)
		}
		if !strings.HasPrefix(string(body), "stem-") {
			t.Errorf("stem %s should be a byte copy, got %q", o.Name(), body)
		}
	}
}

// Post-processing changes the audio, so a copy is no longer correct.
func TestNormalizeForcesStemsThroughFFmpeg(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	runPipeline(t, f, Request{OutputDir: dir, Normalize: true})

	var sawLoudnorm bool
	for _, c := range f.CallsTo("ffmpeg") {
		if strings.Contains(strings.Join(c.Args, " "), "loudnorm") {
			sawLoudnorm = true
		}
	}
	if !sawLoudnorm {
		t.Error("expected loudnorm in the ffmpeg filter chain when Normalize is set")
	}
}

// Separation costs minutes; a second run must reuse what is already on disk.
func TestRerunReusesDownloadAndStems(t *testing.T) {
	dir := t.TempDir()

	first := newFake(t, defaultStems)
	runPipeline(t, first, Request{OutputDir: dir})
	if got := len(first.CallsTo("demucs")); got != 1 {
		t.Fatalf("first run made %d demucs calls, want 1", got)
	}

	second := newFake(t, defaultStems)
	res := runPipeline(t, second, Request{OutputDir: dir})

	if got := len(second.CallsTo("demucs")); got != 0 {
		t.Errorf("second run re-ran demucs %d times; existing stems should be reused", got)
	}
	for _, c := range second.CallsTo("yt-dlp") {
		if c.HasArg("--output") {
			t.Error("second run re-downloaded the source; the existing file should be reused")
		}
	}
	if len(res.Outputs) != 1+len(defaultStems)*2 {
		t.Errorf("second run produced %d outputs, want %d", len(res.Outputs), 1+len(defaultStems)*2)
	}
}

// A 6-stem model yields more minus-one mixes with no special-casing.
func TestSixStemModelYieldsMoreMixes(t *testing.T) {
	dir := t.TempDir()
	stems := []string{"bass", "drums", "guitar", "other", "piano", "vocals"}
	f := newFake(t, stems)
	res := runPipeline(t, f, Request{OutputDir: dir, Model: separate.ModelSixStem})

	if want := 1 + len(stems)*2; len(res.Outputs) != want {
		t.Fatalf("got %d outputs, want %d", len(res.Outputs), want)
	}
	if got := names(res.Outputs); !containsString(got, "AcaoDeGracasLiveNoGuitar.wav") {
		t.Errorf("expected a NoGuitar mix, got %v", got)
	}
}

func TestProgressReachesEveryStage(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)

	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	completed := map[Stage]bool{}
	observe := func(e Event) {
		<-mu
		if e.Done {
			completed[e.Stage] = true
		}
		mu <- struct{}{}
	}

	if _, err := newPipeline(f).Run(context.Background(),
		Request{URL: "https://youtu.be/" + testID, OutputDir: dir}, observe); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	for _, s := range []Stage{StageInspect, StageDownload, StageSeparate, StageRender} {
		if !completed[s] {
			t.Errorf("stage %q never reported completion", s)
		}
	}
}

func TestExportCopiesToUSBRoot(t *testing.T) {
	dir := t.TempDir()
	usb := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{OutputDir: dir, USBPath: usb})

	if res.Exported != usb {
		t.Errorf("Exported = %q, want %q", res.Exported, usb)
	}
	for _, o := range res.Outputs {
		// The module only finds audio in the drive's root, never a subfolder.
		if _, err := os.Stat(filepath.Join(usb, o.Name())); err != nil {
			t.Errorf("%s was not copied to the USB root: %v", o.Name(), err)
		}
	}
}

func TestExportRejectsMissingDestination(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	_, err := newPipeline(f).Run(context.Background(), Request{
		URL:       "https://youtu.be/" + testID,
		OutputDir: dir,
		USBPath:   filepath.Join(dir, "no-such-drive"),
	}, nil)
	if err == nil {
		t.Fatal("expected an error for a non-existent USB destination")
	}
}

func TestCancellationStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newPipeline(f).Run(ctx, Request{
		URL: "https://youtu.be/" + testID, OutputDir: dir,
	}, nil); err == nil {
		t.Fatal("expected a cancelled context to abort the run")
	}
}

func TestSlugIsStableAndScoped(t *testing.T) {
	tests := []struct {
		title, id, want string
	}{
		{"Ação de Graças (Live)", "abc123", "a-o-de-gra-as-live-abc123"},
		{"Simple Title", "xyz", "simple-title-xyz"},
		{"", "onlyid", "track-onlyid"},
	}
	for _, tt := range tests {
		if got := slug(tt.title, tt.id); got != tt.want {
			t.Errorf("slug(%q, %q) = %q, want %q", tt.title, tt.id, got, tt.want)
		}
	}
}

// --- helpers ---

func probeJSON(codec string, rate, channels int, duration string) string {
	return `{"streams":[{"codec_name":"` + codec + `","sample_rate":"` +
		itoa(rate) + `","channels":` + itoa(channels) + `}],"format":{"duration":"` + duration + `"}}`
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

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func callArgAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func inputPaths(c runnertest.Call) []string {
	var out []string
	for i, a := range c.Args {
		if a == "-i" && i+1 < len(c.Args) {
			out = append(out, c.Args[i+1])
		}
	}
	return out
}

func lastArg(c runnertest.Call) string {
	if len(c.Args) == 0 {
		return ""
	}
	return c.Args[len(c.Args)-1]
}

func names(outs []Output) []string {
	n := make([]string, len(outs))
	for i, o := range outs {
		n[i] = o.Name()
	}
	return n
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// --- multi-format output ---

func TestExtraFormatsLandInTheirOwnFolders(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{
		OutputDir: dir,
		Formats:   []string{audio.EncodingFLAC, audio.EncodingOpus},
	})

	tracks := 1 + len(defaultStems)*2
	// WAV plus the two requested formats, every track in each.
	if want := tracks * 3; len(res.Outputs) != want {
		t.Fatalf("got %d outputs, want %d", len(res.Outputs), want)
	}

	for _, id := range []string{audio.EncodingWAV, audio.EncodingFLAC, audio.EncodingOpus} {
		enc, _ := audio.LookupEncoding(id)
		var seen int
		for _, o := range res.Outputs {
			if o.Encoding != id {
				continue
			}
			seen++
			if got := filepath.Base(filepath.Dir(o.Path)); got != enc.Dir {
				t.Errorf("%s written to %q, want folder %q", o.Name(), got, enc.Dir)
			}
			if ext := filepath.Ext(o.Path); ext != "."+enc.Extension {
				t.Errorf("%s has extension %q, want %q", o.Name(), ext, "."+enc.Extension)
			}
			if _, err := os.Stat(o.Path); err != nil {
				t.Errorf("%s was not written: %v", o.Path, err)
			}
		}
		if seen != tracks {
			t.Errorf("encoding %s produced %d files, want %d", id, seen, tracks)
		}
	}
}

// The compressed copies must carry the same audio as the WAVs, so they are
// derived from the rendered WAV rather than re-mixed from stems.
func TestExtraFormatsAreEncodedFromTheRenderedWAV(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{OutputDir: dir, Formats: []string{audio.EncodingMP3}})

	wavPaths := map[string]bool{}
	for _, o := range res.Outputs {
		if o.Encoding == audio.EncodingWAV {
			wavPaths[o.Path] = true
		}
	}

	var checked int
	for _, c := range f.CallsTo("ffmpeg") {
		dst := lastArg(c)
		if filepath.Ext(dst) != ".mp3" {
			continue
		}
		checked++
		inputs := inputPaths(c)
		if len(inputs) != 1 || !wavPaths[inputs[0]] {
			t.Errorf("%s encoded from %v, want a single rendered WAV", filepath.Base(dst), inputs)
		}
	}
	if checked == 0 {
		t.Fatal("expected mp3 encode calls")
	}
}

func TestUnknownFormatFailsBeforeAnyWork(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	_, err := newPipeline(f).Run(context.Background(), Request{
		URL: "https://youtu.be/" + testID, OutputDir: dir, Formats: []string{"wma"},
	}, nil)
	if err == nil {
		t.Fatal("expected an unknown format to be rejected")
	}
	// Separation is the expensive stage; it must not run for a typo.
	if got := len(f.CallsTo("demucs")); got != 0 {
		t.Errorf("ran demucs %d times despite an invalid format", got)
	}
}

// The module plays WAV only, so shareable copies must stay off the drive.
func TestExportSendsOnlyWAVToUSB(t *testing.T) {
	dir := t.TempDir()
	usb := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{
		OutputDir: dir, USBPath: usb,
		Formats: []string{audio.EncodingFLAC, audio.EncodingMP3},
	})

	entries, err := os.ReadDir(usb)
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + len(defaultStems)*2; len(entries) != want {
		t.Errorf("copied %d files to the drive, want %d (WAV only)", len(entries), want)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".wav" {
			t.Errorf("%s was copied to the drive, but the module only plays WAV", e.Name())
		}
	}

	// The other formats must still exist locally.
	var flac int
	for _, o := range res.Outputs {
		if o.Encoding == audio.EncodingFLAC {
			flac++
		}
	}
	if flac == 0 {
		t.Error("FLAC files should still be produced locally")
	}
}

func TestByEncodingRollsUpPerFormat(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)
	res := runPipeline(t, f, Request{
		OutputDir: dir,
		Formats:   []string{audio.EncodingMP3, audio.EncodingFLAC},
	})

	groups := res.ByEncoding()
	if len(groups) != 3 {
		t.Fatalf("got %d format groups, want 3", len(groups))
	}
	// Registry order, regardless of the order they were requested in.
	want := []string{audio.EncodingWAV, audio.EncodingFLAC, audio.EncodingMP3}
	for i, g := range groups {
		if g.Encoding.ID != want[i] {
			t.Errorf("group %d is %q, want %q", i, g.Encoding.ID, want[i])
		}
		if g.Count != 1+len(defaultStems)*2 {
			t.Errorf("group %q has %d files, want %d", g.Encoding.ID, g.Count, 1+len(defaultStems)*2)
		}
		if g.Bytes <= 0 {
			t.Errorf("group %q reports %d bytes", g.Encoding.ID, g.Bytes)
		}
	}
}

func TestEncodeStageReportsCompletion(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, defaultStems)

	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	completed := map[Stage]bool{}
	observe := func(e Event) {
		<-gate
		if e.Done {
			completed[e.Stage] = true
		}
		gate <- struct{}{}
	}

	if _, err := newPipeline(f).Run(context.Background(), Request{
		URL: "https://youtu.be/" + testID, OutputDir: dir,
		Formats: []string{audio.EncodingOpus},
	}, observe); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if !completed[StageEncode] {
		t.Error("the encoding stage never reported completion")
	}
}
