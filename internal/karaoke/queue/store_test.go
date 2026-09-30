package queue

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/song"
)

const (
	urlA = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	urlB = "https://www.youtube.com/watch?v=bbbbbbbbbbb"
	urlC = "https://www.youtube.com/watch?v=ccccccccccc"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "queue.json")
	s, w, err := Open(path)
	if err != nil || w != nil {
		t.Fatalf("Open = warning %v, err %v", w, err)
	}
	return s, path
}

func reopen(t *testing.T, path string) *Store {
	t.Helper()
	s, w, err := Open(path)
	if err != nil || w != nil {
		t.Fatalf("reopen = warning %v, err %v", w, err)
	}
	return s
}

func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func mustAdd(t *testing.T, s *Store, url string) Entry {
	t.Helper()
	e, err := s.Add(url)
	if err != nil {
		t.Fatalf("Add(%q): %v", url, err)
	}
	return e
}

func TestMissingFileOpensAnEmptyQueue(t *testing.T) {
	s, _ := openStore(t)
	if got := s.Entries(); len(got) != 0 {
		t.Errorf("Entries = %v, want none", got)
	}
}

func TestAddMoveRemoveThenReloadGivesTheIdenticalQueue(t *testing.T) {
	s, path := openStore(t)
	s.Now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	a := mustAdd(t, s, urlA)
	b := mustAdd(t, s, urlB)
	c := mustAdd(t, s, urlC)

	if a.ID != "aaaaaaaaaaa" || a.VideoID != "aaaaaaaaaaa" || a.State != Queued {
		t.Fatalf("added entry = %+v", a)
	}
	if err := s.Move(c.ID, -2); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(b.ID); err != nil {
		t.Fatal(err)
	}
	s.SetTitle(a.ID, "Song A")
	s.Finish(c.ID, "/songs/c", "Song C")
	s.RecordScore(c.ID, Score{At: s.now(), Difficulty: "normal", Completed: true,
		Players: []Player{{Name: "Ana", Score: 8100}, {Name: "Bo", Score: 7000}}})

	want := s.Entries()
	if got := ids(want); !reflect.DeepEqual(got, []string{c.ID, a.ID}) {
		t.Fatalf("order = %v, want [c a]", got)
	}
	got := reopen(t, path).Entries()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded queue differs\n got: %+v\nwant: %+v", got, want)
	}
}

func TestMoveClampsAtBothEnds(t *testing.T) {
	s, _ := openStore(t)
	a, b, c := mustAdd(t, s, urlA), mustAdd(t, s, urlB), mustAdd(t, s, urlC)
	s.Move(a.ID, 99)
	if got := ids(s.Entries()); !reflect.DeepEqual(got, []string{b.ID, c.ID, a.ID}) {
		t.Errorf("after moving a far down: %v", got)
	}
	s.Move(a.ID, -99)
	if got := ids(s.Entries()); !reflect.DeepEqual(got, []string{a.ID, b.ID, c.ID}) {
		t.Errorf("after moving a far up: %v", got)
	}
	s.Move(b.ID, 1)
	if got := ids(s.Entries()); !reflect.DeepEqual(got, []string{a.ID, c.ID, b.ID}) {
		t.Errorf("after moving b down one: %v", got)
	}
}

func TestMutatingAMissingEntryReportsNotFound(t *testing.T) {
	s, _ := openStore(t)
	for name, err := range map[string]error{
		"remove":  s.Remove("nope"),
		"move":    s.Move("nope", 1),
		"state":   s.SetState("nope", Ready),
		"title":   s.SetTitle("nope", "x"),
		"fail":    s.Fail("nope", errors.New("x")),
		"score":   s.RecordScore("nope", Score{}),
		"songdir": s.SetSongDir("nope", "x"),
		"finish":  s.Finish("nope", "x", "y"),
		"stage":   s.SetStage("nope", song.StageDownload),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestEntriesReturnsACopyCallersCannotUseToChangeTheQueue(t *testing.T) {
	s, _ := openStore(t)
	e := mustAdd(t, s, urlA)
	s.RecordScore(e.ID, Score{Players: []Player{{Name: "Ana", Score: 1}}})

	snap := s.Entries()
	snap[0].Title = "hacked"
	snap[0].Scores[0].Players[0].Name = "hacked"

	got := s.Entries()[0]
	if got.Title != "" || got.Scores[0].Players[0].Name != "Ana" {
		t.Errorf("internal state changed through a snapshot: %+v", got)
	}
}

func TestALeftoverTempFileFromACrashMidWriteIsIgnored(t *testing.T) {
	s, path := openStore(t)
	a := mustAdd(t, s, urlA)

	// A crash after the temp file was created but before the rename.
	torn := path + ".123456.tmp"
	if err := os.WriteFile(torn, []byte(`{"version":1,"entries":[{"id":"x`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := reopen(t, path).Entries()
	if len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("Entries = %+v, want only %s from the last complete file", got, a.ID)
	}
}

func TestAPreparingEntryBecomesQueuedOnReload(t *testing.T) {
	s, path := openStore(t)
	a := mustAdd(t, s, urlA)
	if _, ok := s.ClaimNext(); !ok {
		t.Fatal("ClaimNext found nothing")
	}
	if e, _ := s.Get(a.ID); e.State != Preparing {
		t.Fatalf("state = %s, want preparing", e.State)
	}

	re := reopen(t, path)
	if e, _ := re.Get(a.ID); e.State != Queued {
		t.Errorf("state after reload = %s, want queued", e.State)
	}
	select {
	case <-re.WorkAvailable():
	default:
		t.Error("reloading a queued entry should wake the worker")
	}
}

func TestTheSameVideoUnderAnotherURLFormIsADuplicate(t *testing.T) {
	s, _ := openStore(t)
	first := mustAdd(t, s, urlA)
	s.SetTitle(first.ID, "Song A")

	for _, dup := range []string{
		"https://youtu.be/aaaaaaaaaaa",
		"https://www.youtube.com/watch?v=aaaaaaaaaaa&list=PLx&t=42",
		"https://youtube.com/shorts/aaaaaaaaaaa",
		urlA,
	} {
		_, err := s.Add(dup)
		var de *DuplicateError
		if !errors.As(err, &de) {
			t.Fatalf("Add(%q) err = %v, want *DuplicateError", dup, err)
		}
		if de.Existing.ID != first.ID || de.Existing.Title != "Song A" {
			t.Errorf("Add(%q) existing = %+v", dup, de.Existing)
		}
	}
	if n := len(s.Entries()); n != 1 {
		t.Errorf("len = %d, want 1", n)
	}
}

func TestNonYouTubeURLsGetAStableIDAndDuplicatesAreCaught(t *testing.T) {
	s, path := openStore(t)
	e := mustAdd(t, s, "https://example.com/video/1")
	if e.VideoID != "" || !strings.HasPrefix(e.ID, "url-") {
		t.Errorf("entry = %+v", e)
	}
	if again := reopen(t, path); again.Entries()[0].ID != e.ID {
		t.Error("ID changed across reload")
	}
	var de *DuplicateError
	if _, err := s.Add(" https://example.com/video/1 "); !errors.As(err, &de) {
		t.Errorf("err = %v, want duplicate", err)
	}
	if _, err := s.Add("   "); err == nil {
		t.Error("blank URL should be rejected")
	}
}

func TestACorruptFileIsSetAsideAndAnEmptyQueueStarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	os.WriteFile(path, []byte(`{"version":1,"entries":[{"id":`), 0o644)

	s, w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || !strings.HasPrefix(filepath.Base(w.MovedTo), "queue.json.corrupt-") {
		t.Fatalf("warning = %+v", w)
	}
	if data, err := os.ReadFile(w.MovedTo); err != nil || !strings.Contains(string(data), `"entries"`) {
		t.Errorf("the corrupt file was not preserved: %v %q", err, data)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the corrupt file is still at the queue path: %v", err)
	}
	if len(s.Entries()) != 0 {
		t.Error("queue should start empty")
	}
	// And the store works normally afterwards.
	mustAdd(t, s, urlA)
	if len(reopen(t, path).Entries()) != 1 {
		t.Error("new entries did not persist after recovery")
	}
}

func TestAFileWithoutAVersionIsTreatedAsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	os.WriteFile(path, []byte(`{"entries":[]}`), 0o644)
	if _, w, err := Open(path); err != nil || w == nil {
		t.Errorf("warning = %v, err = %v", w, err)
	}
}

func TestANewerSchemaVersionIsRejectedAndTheFileStaysUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	content := `{"version":99,"entries":[{"id":"future"}]}`
	os.WriteFile(path, []byte(content), 0o644)

	s, w, err := Open(path)
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Found != 99 || ve.Want != Version {
		t.Fatalf("err = %v, want *VersionError", err)
	}
	if s != nil || w != nil {
		t.Errorf("store %v / warning %v should be nil", s, w)
	}
	if data, _ := os.ReadFile(path); string(data) != content {
		t.Errorf("file was modified: %q", data)
	}
	if m, _ := filepath.Glob(path + ".corrupt-*"); len(m) != 0 {
		t.Errorf("file was renamed aside: %v", m)
	}
}

func TestFailRecordsTheMessageAndStageAndRetryClearsIt(t *testing.T) {
	s, path := openStore(t)
	a := mustAdd(t, s, urlA)
	s.Fail(a.ID, &song.StageError{Stage: song.StageSeparate, Err: errors.New("demucs exploded")})

	e := reopen(t, path).Entries()[0]
	if e.State != Failed || !strings.Contains(e.Error, "demucs exploded") || e.Stage != song.StageSeparate {
		t.Fatalf("entry = %+v", e)
	}
	<-s.WorkAvailable() // drain the signal from Add
	s.SetState(a.ID, Queued)
	e, _ = s.Get(a.ID)
	if e.State != Queued || e.Error != "" {
		t.Errorf("after retry: %+v", e)
	}
	select {
	case <-s.WorkAvailable():
	default:
		t.Error("retrying should wake the worker")
	}
}

func TestSetStageOnlyWritesWhenTheStageChanges(t *testing.T) {
	s, path := openStore(t)
	a := mustAdd(t, s, urlA)
	s.SetStage(a.ID, song.StageDownload)
	os.Remove(path)
	s.SetStage(a.ID, song.StageDownload)
	if _, err := os.Stat(path); err == nil {
		t.Error("an unchanged stage rewrote the file")
	}
	s.SetStage(a.ID, song.StageSeparate)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a changed stage did not write: %v", err)
	}
}

func TestClaimNextTakesQueuedEntriesInOrderAndSkipsOthers(t *testing.T) {
	s, _ := openStore(t)
	a, b := mustAdd(t, s, urlA), mustAdd(t, s, urlB)
	s.SetState(a.ID, Ready)

	got, ok := s.ClaimNext()
	if !ok || got.ID != b.ID || got.State != Preparing {
		t.Fatalf("ClaimNext = %+v, %v", got, ok)
	}
	if _, ok := s.ClaimNext(); ok {
		t.Error("nothing should be left to claim")
	}
}
