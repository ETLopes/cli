package song

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/reference"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/youtube"
)

const (
	testID    = "abc123DEF45"
	testURL   = "https://www.youtube.com/watch?v=" + testID
	testTitle = "Placeholder Artist - Placeholder Title (Official Video)"
	testDir   = "placeholder-artist-placeholder-title-official-video-" + testID

	contourJSON = `{"hop":0.016,"sample_rate":16000,"time":[0,0.016,0.032,0.048],"hz":[220,220,220,220],"confidence":[0.9,0.9,0.9,0.9],"loudness_db":[-20,-20,-20,-20]}`
	syncedLRC   = "[00:01.00]la la la\n[00:03.00]placeholder words\n"
)

// harness wires a Preparer to a fake command runner and a fake LRCLIB, so a
// whole preparation runs with no tools installed and no network.
type harness struct {
	t     *testing.T
	songs string
	fake  *runnertest.Fake
	prep  *Preparer

	mu     sync.Mutex
	hits   []string // LRCLIB request paths, in order
	events []Event

	// LRCLIB behaviour; tests replace these before Prepare.
	lrclibGet    func(w http.ResponseWriter, r *http.Request)
	lrclibSearch func(w http.ResponseWriter, r *http.Request)
	srv          *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, songs: t.TempDir()}
	h.lrclibGet = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":1,"duration":200,"instrumental":false,"syncedLyrics":%q}`, syncedLRC)
	}
	h.lrclibSearch = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }

	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits = append(h.hits, r.URL.Path)
		h.mu.Unlock()
		switch r.URL.Path {
		case "/api/get":
			h.lrclibGet(w, r)
		case "/api/search":
			h.lrclibSearch(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)

	h.reset()
	return h
}

// reset installs a fresh runner (and so a fresh call log) with the standard
// tool behaviour, keeping the songs directory and LRCLIB hit log. It models a
// new process resuming earlier work.
func (h *harness) reset() {
	f := runnertest.New()
	touch := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}

	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: fmt.Sprintf(`{"id":%q,"title":%q,"uploader":"Some Channel","duration":200,"webpage_url":%q}`, testID, testTitle, testURL),
	})
	f.HandleFunc("yt-dlp", []string{"--output"}, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(spec runner.Spec) error {
			out, _ := runnertest.Call{Args: spec.Args}.ArgAfter("--output")
			return touch(strings.Replace(out, "%(ext)s", "m4a", 1), "audio")
		}}
	})
	f.HandleFunc("demucs", nil, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(spec runner.Spec) error {
			c := runnertest.Call{Args: spec.Args}
			out, _ := c.ArgAfter("--out")
			model, _ := c.ArgAfter("--name")
			for _, name := range []string{"vocals.wav", "no_vocals.wav"} {
				if err := touch(filepath.Join(out, model, name), "wav"); err != nil {
					return err
				}
			}
			return nil
		}}
	})
	writeLast := func(content string) func(runner.Spec) runnertest.Response {
		return func(runner.Spec) runnertest.Response {
			return runnertest.Response{Do: func(spec runner.Spec) error {
				return touch(spec.Args[len(spec.Args)-1], content)
			}}
		}
	}
	f.HandleFunc("ffmpeg", nil, writeLast("RIFF"))
	f.HandleFunc("venv-python", nil, writeLast(contourJSON))

	h.fake = f
	h.prep = &Preparer{
		YouTube:   youtube.New(f),
		Separator: separate.New(f),
		Audio:     audio.New(f),
		Reference: &reference.SwiftF0{Run: f, Python: "venv-python"},
		Lyrics:    &lyrics.Client{BaseURL: h.srv.URL + "/api"},
		Now:       func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
	}
}

func (h *harness) prepare() (Song, error) {
	h.t.Helper()
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	return h.prep.Prepare(context.Background(), testURL, h.songs, func(e Event) {
		h.mu.Lock()
		h.events = append(h.events, e)
		h.mu.Unlock()
	})
}

func (h *harness) mustPrepare() Song {
	h.t.Helper()
	s, err := h.prepare()
	if err != nil {
		h.t.Fatalf("Prepare: %v", err)
	}
	return s
}

// commands names each external call by what it did, in order.
func (h *harness) commands() []string {
	var out []string
	for _, c := range h.fake.Calls() {
		switch {
		case strings.Contains(c.Name, "yt-dlp") && c.HasArg("--dump-single-json"):
			out = append(out, "inspect")
		case strings.Contains(c.Name, "yt-dlp"):
			out = append(out, "download")
		case strings.Contains(c.Name, "demucs"):
			out = append(out, "demucs")
		case strings.Contains(c.Name, "ffmpeg"):
			out = append(out, "ffmpeg")
		case strings.Contains(c.Name, "python"):
			out = append(out, "python")
		default:
			out = append(out, c.Name)
		}
	}
	return out
}

func (h *harness) lrclibHits() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.hits...)
}

func (h *harness) songDir() string { return filepath.Join(h.songs, testDir) }

func (h *harness) exists(rel string) bool {
	info, err := os.Stat(filepath.Join(h.songDir(), rel))
	return err == nil && info.Size() > 0
}

func TestPrepareRunsEveryStageInOrderAndWritesTheArtifacts(t *testing.T) {
	h := newHarness(t)
	s := h.mustPrepare()

	want := []string{"inspect", "download", "demucs", "ffmpeg", "ffmpeg", "python"}
	if got := h.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if got := h.lrclibHits(); !reflect.DeepEqual(got, []string{"/api/get"}) {
		t.Errorf("LRCLIB hits = %v", got)
	}

	if s.Dir != h.songDir() {
		t.Errorf("Dir = %q, want %q", s.Dir, h.songDir())
	}
	for _, rel := range []string{"song.json", "source.m4a", "stems/2stems-vocals/htdemucs/vocals.wav",
		"stems/2stems-vocals/htdemucs/no_vocals.wav", "instrumental.wav", "vocals16k.wav", "reference.json", "lyrics.lrc"} {
		if !h.exists(rel) {
			t.Errorf("%s was not written", rel)
		}
	}
	if !s.Ready() {
		t.Error("a fully prepared song should be Ready")
	}
	if s.Lyrics != LyricsSynced {
		t.Errorf("Lyrics = %q", s.Lyrics)
	}
	for _, st := range Stages {
		if !s.complete(s.Dir, st) {
			t.Errorf("stage %s not complete in the manifest", st)
		}
	}
	if s.VideoID != testID || s.Track != "Placeholder Title" || s.Artist != "Placeholder Artist" || s.Duration() != 200*time.Second {
		t.Errorf("metadata = %+v", s.Manifest)
	}
	if lrc, _ := os.ReadFile(filepath.Join(s.Dir, "lyrics.lrc")); string(lrc) != syncedLRC {
		t.Errorf("lyrics.lrc = %q", lrc)
	}
}

func TestPrepareAsksDemucsForTwoStemVocalsAndRendersTheDocumentedFormats(t *testing.T) {
	h := newHarness(t)
	h.mustPrepare()

	var demucs, instrumental, vocals *runnertest.Call
	for _, c := range h.fake.Calls() {
		c := c
		switch {
		case strings.Contains(c.Name, "demucs"):
			demucs = &c
		case strings.Contains(c.Name, "ffmpeg") && strings.HasSuffix(c.Args[len(c.Args)-1], "instrumental.wav.part"):
			instrumental = &c
		case strings.Contains(c.Name, "ffmpeg"):
			vocals = &c
		}
	}
	if demucs == nil || instrumental == nil || vocals == nil {
		t.Fatalf("missing calls: %v", h.commands())
	}
	if v, _ := demucs.ArgAfter("--two-stems"); v != "vocals" {
		t.Errorf("--two-stems = %q", v)
	}
	if v, _ := demucs.ArgAfter("--name"); v != "htdemucs" {
		t.Errorf("--name = %q", v)
	}
	check := func(name string, c *runnertest.Call, rate, ch, codec string) {
		for flag, want := range map[string]string{"-ar": rate, "-ac": ch, "-c:a": codec} {
			if got, _ := c.ArgAfter(flag); got != want {
				t.Errorf("%s %s = %q, want %q", name, flag, got, want)
			}
		}
	}
	check("instrumental", instrumental, "48000", "2", "pcm_f32le")
	check("vocals", vocals, "16000", "1", "pcm_s16le")
	if in, _ := instrumental.ArgAfter("-i"); !strings.HasSuffix(in, "no_vocals.wav") {
		t.Errorf("instrumental rendered from %q, want the no_vocals stem", in)
	}
	if in, _ := vocals.ArgAfter("-i"); !strings.HasSuffix(in, "/vocals.wav") {
		t.Errorf("vocals rendered from %q, want the vocals stem", in)
	}
}

func TestPrepareGivesTheReferenceExtractorTheRenderedVocals(t *testing.T) {
	h := newHarness(t)
	h.mustPrepare()
	var py runnertest.Call
	for _, c := range h.fake.Calls() {
		if strings.Contains(c.Name, "python") {
			py = c
		}
	}
	if len(py.Args) != 4 || py.Args[2] != filepath.Join(h.songDir(), "vocals16k.wav") {
		t.Errorf("python args = %q", py.Args)
	}
	c, err := reference.Load(filepath.Join(h.songDir(), "reference.json"))
	if err != nil || c.Len() != 4 {
		t.Errorf("reference.json unreadable: %v", err)
	}
}

func TestPrepareOfACompleteSongRunsNothingAndMakesNoHTTPCall(t *testing.T) {
	h := newHarness(t)
	first := h.mustPrepare()
	hitsBefore := len(h.lrclibHits())
	h.reset() // a new process

	second := h.mustPrepare()
	if got := h.commands(); len(got) != 0 {
		t.Errorf("a complete song ran %v", got)
	}
	if got := len(h.lrclibHits()); got != hitsBefore {
		t.Errorf("a complete song made %d new HTTP calls", got-hitsBefore)
	}
	if second.Dir != first.Dir || second.Title != first.Title || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("second = %+v", second)
	}
	for _, e := range h.events {
		if e.Done && !e.Cached {
			t.Errorf("stage %s should be reported as cached: %+v", e.Stage, e)
		}
	}
}

func TestPrepareResumesAtRenderAfterACrashFollowingSeparation(t *testing.T) {
	h := newHarness(t)
	s := h.mustPrepare()

	// Simulate a crash: the manifest only knows about the first three stages.
	m := s.Manifest
	for _, st := range []Stage{StageRender, StageReference, StageLyrics} {
		delete(m.Stages, st)
	}
	if err := writeManifest(s.Dir, m); err != nil {
		t.Fatal(err)
	}
	h.reset()

	h.mustPrepare()
	if got, want := h.commands(), []string{"ffmpeg", "ffmpeg", "python"}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v (no download, no demucs)", got, want)
	}
}

func TestPrepareRedoesAStageWhoseArtifactIsEmpty(t *testing.T) {
	h := newHarness(t)
	h.mustPrepare()
	h.reset()
	if err := os.WriteFile(filepath.Join(h.songDir(), "instrumental.wav"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustPrepare()
	if got, want := h.commands(), []string{"ffmpeg", "ffmpeg"}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want only the render redone %v", got, want)
	}
	if !h.exists("instrumental.wav") {
		t.Error("instrumental.wav was not rewritten")
	}
}

func TestPrepareRedoesAStageWhoseArtifactIsMissing(t *testing.T) {
	h := newHarness(t)
	h.mustPrepare()
	h.reset()
	if err := os.Remove(filepath.Join(h.songDir(), "stems/2stems-vocals/htdemucs/no_vocals.wav")); err != nil {
		t.Fatal(err)
	}

	h.mustPrepare()
	got := h.commands()
	if len(got) != 1 || got[0] != "demucs" {
		t.Errorf("commands = %v, want only demucs", got)
	}
	if !h.exists("stems/2stems-vocals/htdemucs/no_vocals.wav") {
		t.Error("the stem was not regenerated")
	}
}

func TestPrepareStillInspectsWhenTheURLHasNoVideoID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.prep.Prepare(ctx, "https://example.com/some-video", h.songs, nil); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if _, err := h.prep.Prepare(ctx, "https://example.com/some-video", h.songs, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.commands(); !reflect.DeepEqual(got, []string{"inspect"}) {
		t.Errorf("commands = %v, want a lone inspect to find the song", got)
	}
}

func TestPrepareFindsAnExistingSongByIDEvenIfTheTitleChanged(t *testing.T) {
	h := newHarness(t)
	s := h.mustPrepare()
	old := filepath.Join(h.songs, "an-older-title-"+testID)
	if err := os.Rename(s.Dir, old); err != nil {
		t.Fatal(err)
	}
	h.reset()

	got := h.mustPrepare()
	if got.Dir != old {
		t.Errorf("Dir = %q, want the existing %q", got.Dir, old)
	}
	if cmds := h.commands(); len(cmds) != 0 {
		t.Errorf("ran %v", cmds)
	}
}

func TestPrepareIsReadyWithoutLyricsForAnInstrumental(t *testing.T) {
	h := newHarness(t)
	h.lrclibGet = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"duration":200,"instrumental":true,"plainLyrics":null,"syncedLyrics":null}`)
	}
	s := h.mustPrepare()
	if !s.Ready() || s.Lyrics != LyricsNone {
		t.Errorf("Ready=%v Lyrics=%q", s.Ready(), s.Lyrics)
	}
	if s.LyricsNote == "" {
		t.Error("the no-lyrics outcome should say why")
	}
	if h.exists("lyrics.lrc") || h.exists("lyrics.txt") {
		t.Error("no lyrics file should exist")
	}
	if !s.complete(s.Dir, StageLyrics) {
		t.Error("a definitive answer of 'no lyrics' is a finished stage; retrying would only repeat it")
	}
	if _, ok, err := s.LoadLyrics(); ok || err != nil {
		t.Errorf("LoadLyrics() ok=%v err=%v", ok, err)
	}
}

func TestPrepareIsReadyWithoutLyricsWhenLRCLIBHasNoMatch(t *testing.T) {
	h := newHarness(t)
	h.lrclibGet = http.NotFound
	s := h.mustPrepare()
	if !s.Ready() || s.Lyrics != LyricsNone {
		t.Errorf("Ready=%v Lyrics=%q", s.Ready(), s.Lyrics)
	}
	if got := h.lrclibHits(); len(got) < 2 || got[1] != "/api/search" {
		t.Errorf("hits = %v, want the search fallback", got)
	}
}

func TestPrepareSavesPlainLyricsWhenThereAreNoSyncedOnes(t *testing.T) {
	h := newHarness(t)
	h.lrclibGet = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"duration":200,"instrumental":false,"plainLyrics":"la la la\nplaceholder words","syncedLyrics":null}`)
	}
	s := h.mustPrepare()
	if s.Lyrics != LyricsPlain {
		t.Errorf("Lyrics = %q", s.Lyrics)
	}
	if !h.exists("lyrics.txt") || h.exists("lyrics.lrc") {
		t.Error("expected only lyrics.txt")
	}
	if _, ok, _ := s.LoadLyrics(); ok {
		t.Error("plain lyrics are not followable, LoadLyrics() must report none")
	}
}

func TestPrepareTreatsALyricsServerErrorAsASoftFailure(t *testing.T) {
	h := newHarness(t)
	h.lrclibGet = func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }

	s := h.mustPrepare()
	if !s.Ready() || s.Lyrics != LyricsNone {
		t.Errorf("Ready=%v Lyrics=%q; a flaky lyrics API must never block singing", s.Ready(), s.Lyrics)
	}
	var warned bool
	for _, e := range h.events {
		if e.Stage == StageLyrics && e.Warning != "" {
			warned = true
		}
	}
	if !warned {
		t.Error("expected a warning event")
	}
	if s.complete(s.Dir, StageLyrics) {
		t.Error("a failed lookup must not be recorded as finished, or it would never be retried")
	}

	// The API recovers: a rerun retries only the lyrics.
	h.reset()
	h.lrclibGet = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":1,"duration":200,"syncedLyrics":%q}`, syncedLRC)
	}
	s = h.mustPrepare()
	if s.Lyrics != LyricsSynced || !h.exists("lyrics.lrc") {
		t.Errorf("Lyrics = %q after the retry", s.Lyrics)
	}
	if cmds := h.commands(); len(cmds) != 0 {
		t.Errorf("the retry ran %v", cmds)
	}
}

func TestPrepareTreatsANetworkErrorAsASoftFailure(t *testing.T) {
	h := newHarness(t)
	h.srv.Close()

	s, err := h.prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !s.Ready() || s.Lyrics != LyricsNone {
		t.Errorf("Ready=%v Lyrics=%q", s.Ready(), s.Lyrics)
	}
	var warned bool
	for _, e := range h.events {
		warned = warned || e.Warning != ""
	}
	if !warned {
		t.Error("expected a warning event")
	}
}

func TestPrepareFailsTheReferenceStageWithTheScriptTailAndKeepsEarlierStages(t *testing.T) {
	h := newHarness(t)
	// The standard fake succeeds everywhere; swap in an extractor whose
	// interpreter fails the way a venv missing swift-f0 does.
	failing := runnertest.New()
	h.prep.Reference = &reference.SwiftF0{Run: failing, Python: "venv-python"}
	failing.Handle("venv-python", nil, runnertest.Response{Err: &runner.ExitError{
		Command: "venv-python -c ...", ExitCode: 1,
		Output: []string{"Traceback (most recent call last):", "ModuleNotFoundError: No module named 'swift_f0'"},
	}})

	_, err := h.prepare()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "No module named 'swift_f0'") {
		t.Errorf("error lost the script tail: %v", err)
	}
	var se *StageError
	if !errors.As(err, &se) || se.Stage != StageReference {
		t.Errorf("err = %#v, want a StageError for the reference stage", err)
	}

	s, loadErr := Load(h.songDir())
	if loadErr != nil {
		t.Fatalf("the manifest should survive the failure: %v", loadErr)
	}
	for _, st := range []Stage{StageInspect, StageDownload, StageSeparate, StageRender} {
		if !s.complete(s.Dir, st) {
			t.Errorf("stage %s should stay recorded", st)
		}
	}
	for _, st := range []Stage{StageReference, StageLyrics} {
		if s.complete(s.Dir, st) {
			t.Errorf("stage %s must not be recorded", st)
		}
	}
	if s.Ready() {
		t.Error("a song without a reference is not Ready")
	}
	if got := h.lrclibHits(); len(got) != 0 {
		t.Errorf("lyrics ran after a failed stage: %v", got)
	}
}

func TestPrepareEmitsStageEventsInOrder(t *testing.T) {
	h := newHarness(t)
	h.mustPrepare()

	var done []Stage
	for _, e := range h.events {
		if e.Done {
			done = append(done, e.Stage)
		}
	}
	if !reflect.DeepEqual(done, Stages) {
		t.Errorf("stages finished in order %v, want %v", done, Stages)
	}
}

func TestPrepareWithoutAnObserverDoesNotPanic(t *testing.T) {
	h := newHarness(t)
	if _, err := h.prep.Prepare(context.Background(), testURL, h.songs, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareStopsWhenTheContextIsCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.prep.Prepare(ctx, testURL, h.songs, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestPreparedSongLoadsIdenticallyFromDisk(t *testing.T) {
	h := newHarness(t)
	s := h.mustPrepare()
	loaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Dir != s.Dir || !loaded.Ready() || loaded.Lyrics != s.Lyrics || loaded.Duration() != s.Duration() {
		t.Errorf("loaded = %+v", loaded)
	}
	// Artifacts are stored relative to the song dir.
	for st, rec := range loaded.Stages {
		for _, a := range rec.Artifacts {
			if filepath.IsAbs(a) {
				t.Errorf("stage %s stores absolute artifact %q", st, a)
			}
		}
	}
}
