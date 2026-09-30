package song

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/i18n"
)

func sampleManifest() Manifest {
	return Manifest{
		Version:    ManifestVersion,
		VideoID:    "abc123DEF45",
		URL:        "https://www.youtube.com/watch?v=abc123DEF45",
		Title:      "Placeholder Artist - Placeholder Title (Official Video)",
		Artist:     "Placeholder Artist",
		Track:      "Placeholder Title",
		DurationMS: 200500,
		CreatedAt:  time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC),
		Model:      "htdemucs",
		Stages: map[Stage]StageRecord{
			StageInspect:  {CompletedAt: time.Date(2026, 9, 30, 12, 0, 1, 0, time.UTC)},
			StageDownload: {Artifacts: []string{"source.m4a"}, CompletedAt: time.Date(2026, 9, 30, 12, 0, 2, 0, time.UTC)},
			StageSeparate: {Artifacts: []string{"stems/2stems-vocals/htdemucs/vocals.wav", "stems/2stems-vocals/htdemucs/no_vocals.wav"}, CompletedAt: time.Date(2026, 9, 30, 12, 5, 0, 0, time.UTC)},
		},
		Lyrics: LyricsSynced,
	}
}

func TestManifestRoundTripsThroughSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	want := sampleManifest()
	if err := writeManifest(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != dir {
		t.Errorf("Dir = %q", got.Dir)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v (sub-second precision must survive)", got.CreatedAt, want.CreatedAt)
	}
	if got.Duration() != 200500*time.Millisecond {
		t.Errorf("Duration = %v", got.Duration())
	}
	// Compare the serialised forms: time.Time carries a Location pointer that
	// reflect.DeepEqual trips on, while JSON is exactly what "identical" means
	// for a file format.
	gotJSON, _ := json.Marshal(got.Manifest)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("manifest changed in transit:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestManifestStoresRelativePathsAndMillisecondUnits(t *testing.T) {
	dir := t.TempDir()
	if err := writeManifest(dir, sampleManifest()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, dir) {
		t.Error("the manifest must not contain absolute paths: the songs dir is movable")
	}
	if !strings.Contains(text, `"duration_ms": 200500`) {
		t.Errorf("duration should be stored in milliseconds:\n%s", text)
	}
}

func TestLoadFailsWithoutAManifest(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("expected an error")
	}
}

func TestLoadRejectsANewerManifestVersion(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	m.Version = ManifestVersion + 1
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("a manifest from a newer version must not be half-understood")
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected an error")
	}
}

func TestWriteManifestLeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := writeManifest(dir, sampleManifest()); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != ManifestName {
		t.Errorf("dir holds %v, want only %s", entries, ManifestName)
	}
}

func TestStageCompleteRequiresANonEmptyArtifact(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if m.complete(dir, StageDownload) {
		t.Error("the artifact is missing: not complete")
	}
	write("source.m4a", "")
	if m.complete(dir, StageDownload) {
		t.Error("the artifact is empty: not complete")
	}
	write("source.m4a", "audio")
	if !m.complete(dir, StageDownload) {
		t.Error("recorded and present: complete")
	}
	if m.complete(dir, StageRender) {
		t.Error("an unrecorded stage is not complete however many files exist")
	}
	if !m.complete(dir, StageInspect) {
		t.Error("a stage with no artifacts is complete once recorded")
	}
}

func TestStageCompleteNeedsEveryArtifact(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	if err := os.MkdirAll(filepath.Join(dir, "stems/2stems-vocals/htdemucs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stems/2stems-vocals/htdemucs/vocals.wav"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m.complete(dir, StageSeparate) {
		t.Error("no_vocals.wav is missing: separation is not complete")
	}
}

func TestStageLabelsAreTranslatedInBothLanguages(t *testing.T) {
	defer i18n.Use(i18n.Current())
	for _, s := range Stages {
		i18n.Use(i18n.EN)
		en := s.Label()
		i18n.Use(i18n.PT)
		pt := s.Label()
		if en == "" || strings.HasPrefix(en, "karaoke.stage.") {
			t.Errorf("%s has no English label: %q", s, en)
		}
		if pt == en || strings.HasPrefix(pt, "karaoke.stage.") {
			t.Errorf("%s has no Portuguese label: %q", s, pt)
		}
	}
}

func TestStagesRunInPipelineOrder(t *testing.T) {
	want := []Stage{StageInspect, StageDownload, StageSeparate, StageRender, StageReference, StageLyrics}
	if !reflect.DeepEqual(Stages, want) {
		t.Errorf("Stages = %v", Stages)
	}
}

func TestSlugMirrorsThePipelineRule(t *testing.T) {
	tests := map[string]string{
		"Placeholder Artist - Placeholder Title (Official Video)": "placeholder-artist-placeholder-title-official-video-ID1",
		"   ":                    "track-ID1",
		"Ünïcode & symbols!!":    "n-code-symbols-ID1",
		strings.Repeat("a", 100): strings.Repeat("a", 60) + "-ID1",
	}
	for in, want := range tests {
		if got := slug(in, "ID1"); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVideoIDReadsCommonURLForms(t *testing.T) {
	const id = "abc123DEF45"
	for _, u := range []string{
		"https://www.youtube.com/watch?v=" + id,
		"https://www.youtube.com/watch?v=" + id + "&list=PLplaceholder&t=10s",
		"https://youtu.be/" + id,
		"https://youtu.be/" + id + "?si=placeholder",
		"https://music.youtube.com/watch?v=" + id,
		"https://www.youtube.com/shorts/" + id,
		"https://www.youtube.com/embed/" + id,
		"https://m.youtube.com/watch?v=" + id,
	} {
		if got, ok := VideoID(u); !ok || got != id {
			t.Errorf("VideoID(%q) = %q, %v", u, got, ok)
		}
	}
	for _, u := range []string{"", "not a url", "https://example.com/watch?v=" + id, "https://www.youtube.com/watch?v=short", "https://www.youtube.com/playlist?list=PLplaceholder"} {
		if got, ok := VideoID(u); ok {
			t.Errorf("VideoID(%q) = %q, want no ID", u, got)
		}
	}
}

func TestSongPathsAreResolvedAgainstItsDirectory(t *testing.T) {
	dir := t.TempDir()
	s := Song{Dir: dir, Manifest: sampleManifest()}
	if got := s.InstrumentalPath(); got != filepath.Join(dir, "instrumental.wav") {
		t.Errorf("InstrumentalPath = %q", got)
	}
	if got := s.VocalsPath(); got != filepath.Join(dir, "vocals16k.wav") {
		t.Errorf("VocalsPath = %q", got)
	}
	if got := s.ReferencePath(); got != filepath.Join(dir, "reference.json") {
		t.Errorf("ReferencePath = %q", got)
	}
}

func TestSongLoadLyricsReadsAndParsesTheSavedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lyrics.lrc"), []byte("[00:01.00]la la la\n[00:03.00]line two"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Song{Dir: dir, Manifest: sampleManifest()}
	l, ok, err := s.LoadLyrics()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(l.Lines) != 2 || l.Lines[1].Text != "line two" {
		t.Errorf("lines = %+v", l.Lines)
	}
}

func TestSongLoadLyricsReportsNoneWhenTheSongHasNoSyncedLyrics(t *testing.T) {
	m := sampleManifest()
	m.Lyrics = LyricsNone
	if _, ok, err := (Song{Dir: t.TempDir(), Manifest: m}).LoadLyrics(); ok || err != nil {
		t.Errorf("ok=%v err=%v, want a clean none", ok, err)
	}
}
