// Package queue keeps the karaoke song queue on disk and prepares its songs in
// the background.
//
// The Store is the source of truth: every change is written before the call
// returns, so killing the process at any moment loses at most the change in
// flight. The Worker never holds state of its own beyond the job it is
// running; on restart it rediscovers its work from the store, and the song
// manifest lets a half-prepared song resume where it stopped.
package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/song"
)

// Version is the schema version written to the queue file. A file with a
// higher version was written by a newer build and is refused rather than
// misread and overwritten.
const Version = 1

// State is where an entry is in its life.
type State string

const (
	// Queued entries wait for the worker, in queue order.
	Queued State = "queued"
	// Preparing marks the entry the worker is running. It never survives a
	// restart: on load it returns to Queued.
	Preparing State = "preparing"
	// Ready entries are prepared and can be sung.
	Ready State = "ready"
	// Failed entries stopped with an error; setting them Queued retries.
	Failed State = "failed"
	// Sung entries have been performed. They stay playable.
	Sung State = "sung"
)

// Player is one singer's result in a session.
type Player struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Score is the result of one session of a song. Difficulty is a plain string
// so this package need not know the scoring unit's vocabulary.
type Score struct {
	At         time.Time `json:"at"`
	Difficulty string    `json:"difficulty"`
	Players    []Player  `json:"players"`
	// Completed is false for a session that was stopped early.
	Completed bool `json:"completed"`
}

// Entry is one song in the queue.
type Entry struct {
	// ID is the video ID when the URL has one, else a stable hash of the URL.
	ID      string `json:"id"`
	URL     string `json:"url"`
	VideoID string `json:"video_id,omitempty"`
	// Title is empty until the song has been inspected.
	Title string `json:"title,omitempty"`
	State State  `json:"state"`
	// Stage is the current stage while preparing, or the last one reached.
	Stage song.Stage `json:"stage,omitempty"`
	Error string     `json:"error,omitempty"`
	// SongDir is set once the song is prepared.
	SongDir string    `json:"song_dir,omitempty"`
	AddedAt time.Time `json:"added_at"`
	Scores  []Score   `json:"scores,omitempty"`
}

func (e Entry) clone() Entry {
	if e.Scores != nil {
		scores := make([]Score, len(e.Scores))
		for i, s := range e.Scores {
			s.Players = append([]Player(nil), s.Players...)
			scores[i] = s
		}
		e.Scores = scores
	}
	return e
}

// ErrNotFound reports a mutation of an entry that is not in the queue.
var ErrNotFound = errors.New("queue entry not found")

// DuplicateError reports an Add of a song already in the queue. It carries the
// existing entry so a UI can point at it instead of just refusing.
type DuplicateError struct{ Existing Entry }

func (e *DuplicateError) Error() string {
	name := e.Existing.Title
	if name == "" {
		name = e.Existing.URL
	}
	return fmt.Sprintf("already in the queue: %s", name)
}

// VersionError reports a queue file written by a newer build.
type VersionError struct {
	Path        string
	Found, Want int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%s has schema version %d, newer than the %d this build understands; upgrade the program (the file was left untouched)",
		e.Path, e.Found, e.Want)
}

// Warning reports a queue file that could not be read and was set aside. It is
// returned rather than logged so the TUI can tell the user where their old
// queue went.
type Warning struct {
	// MovedTo is where the unreadable file now lives.
	MovedTo string
	Err     error
}

func (w *Warning) Error() string {
	return fmt.Sprintf("queue file was unreadable (%v); kept as %s and started an empty queue", w.Err, w.MovedTo)
}
func (w *Warning) Unwrap() error { return w.Err }

type fileFormat struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Store is the persisted queue. It is safe for concurrent use.
type Store struct {
	path string

	// Now supplies timestamps; nil means time.Now. Tests pin it.
	Now func() time.Time

	mu      sync.Mutex
	entries []Entry

	// work is signalled, without blocking, whenever an entry may have become
	// runnable. Its capacity of one makes a signal sticky, so a worker that is
	// busy when it fires still sees it on its next look.
	work chan struct{}
}

// Open loads the queue at path, creating nothing until the first change. A
// missing file is an empty queue. An unreadable one is renamed aside and
// reported through the returned Warning; a file from a newer build is an
// error and stays untouched.
func Open(path string) (*Store, *Warning, error) {
	s := &Store{path: path, work: make(chan struct{}, 1)}

	// Temp files are left by a crash between create and rename. The rename
	// never happened, so they hold nothing the real file lacks.
	if stale, err := filepath.Glob(path + ".*.tmp"); err == nil {
		for _, f := range stale {
			os.Remove(f)
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading queue: %w", err)
	}

	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return s.setAside(err)
	}
	if f.Version < 1 {
		return s.setAside(errors.New("missing schema version"))
	}
	if f.Version > Version {
		return nil, nil, &VersionError{Path: path, Found: f.Version, Want: Version}
	}

	for i := range f.Entries {
		// The worker that was preparing this entry is gone. The manifest
		// remembers finished stages, so requeueing costs only the unfinished one.
		if f.Entries[i].State == Preparing {
			f.Entries[i].State = Queued
		}
	}
	s.entries = f.Entries
	if s.hasQueued() {
		s.signal()
	}
	return s, nil, nil
}

// setAside renames an unreadable queue file so the user's data survives, and
// returns an empty store.
func (s *Store) setAside(cause error) (*Store, *Warning, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000Z")
	aside := s.path + ".corrupt-" + stamp
	if err := os.Rename(s.path, aside); err != nil {
		return nil, nil, fmt.Errorf("queue file is unreadable (%v) and could not be set aside: %w", cause, err)
	}
	return s, &Warning{MovedTo: aside, Err: cause}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// WorkAvailable returns a channel that receives when an entry may have become
// queued. It is a hint, not a count: a receive means "look again".
func (s *Store) WorkAvailable() <-chan struct{} { return s.work }

func (s *Store) signal() {
	select {
	case s.work <- struct{}{}:
	default:
	}
}

func (s *Store) hasQueued() bool {
	for _, e := range s.entries {
		if e.State == Queued {
			return true
		}
	}
	return false
}

// Entries returns a copy of the queue in order. Callers cannot reach the
// store's own state through it.
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.clone()
	}
	return out
}

// Get returns a copy of one entry.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		return s.entries[i].clone(), true
	}
	return Entry{}, false
}

func (s *Store) index(id string) int {
	for i, e := range s.entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// entryID names a URL: the video ID when it has one, so every form of the
// same link lands on one entry, else a hash so the ID is still stable and
// filesystem-safe.
func entryID(url string) (id, videoID string) {
	if v, ok := song.VideoID(url); ok {
		return v, v
	}
	sum := sha256.Sum256([]byte(url))
	return "url-" + hex.EncodeToString(sum[:6]), ""
}

// Add appends url to the queue. If the same video is already queued under any
// URL form, it returns a *DuplicateError carrying the existing entry.
func (s *Store) Add(url string) (Entry, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return Entry{}, errors.New("empty URL")
	}
	id, videoID := entryID(url)
	var added Entry
	err := s.mutate(func(entries []Entry) ([]Entry, error) {
		for _, e := range entries {
			if e.ID == id {
				return nil, &DuplicateError{Existing: e.clone()}
			}
		}
		added = Entry{ID: id, URL: url, VideoID: videoID, State: Queued, AddedAt: s.now()}
		return append(entries, added), nil
	})
	if err != nil {
		return Entry{}, err
	}
	s.signal()
	return added, nil
}

// Remove deletes an entry. It does not touch the song on disk: the library
// outlives the queue.
func (s *Store) Remove(id string) error {
	return s.update(id, func(entries []Entry, i int) []Entry {
		return append(entries[:i], entries[i+1:]...)
	})
}

// Move shifts an entry by delta places (negative is earlier), stopping at
// either end of the queue.
func (s *Store) Move(id string, delta int) error {
	return s.update(id, func(entries []Entry, i int) []Entry {
		j := min(max(i+delta, 0), len(entries)-1)
		e := entries[i]
		if j < i {
			copy(entries[j+1:i+1], entries[j:i])
		} else {
			copy(entries[i:j], entries[i+1:j+1])
		}
		entries[j] = e
		return entries
	})
}

// SetState changes an entry's state. Leaving Failed clears its error, and
// returning to Queued (a retry) wakes the worker.
func (s *Store) SetState(id string, state State) error {
	err := s.edit(id, func(e *Entry) {
		e.State = state
		if state != Failed {
			e.Error = ""
		}
	})
	if err == nil && state == Queued {
		s.signal()
	}
	return err
}

// SetStage records the stage an entry is in. It is a no-op, and writes
// nothing, when the stage has not changed: progress events arrive many times
// a second but the file only needs to know which stage.
func (s *Store) SetStage(id string, stage song.Stage) error {
	if e, ok := s.Get(id); ok && e.Stage == stage {
		return nil
	}
	return s.edit(id, func(e *Entry) { e.Stage = stage })
}

// SetTitle records the song's title once it is known.
func (s *Store) SetTitle(id, title string) error {
	return s.edit(id, func(e *Entry) { e.Title = title })
}

// SetSongDir records where the prepared song lives.
func (s *Store) SetSongDir(id, dir string) error {
	return s.edit(id, func(e *Entry) { e.SongDir = dir })
}

// Fail marks an entry Failed with err's message. If err says which stage
// failed, the entry's stage becomes that one.
func (s *Store) Fail(id string, err error) error {
	var se *song.StageError
	return s.edit(id, func(e *Entry) {
		e.State = Failed
		e.Error = err.Error()
		if errors.As(err, &se) {
			e.Stage = se.Stage
		}
	})
}

// Finish marks an entry Ready with its song directory and title in a single
// write, so a crash cannot leave a Ready entry without a directory.
func (s *Store) Finish(id, songDir, title string) error {
	return s.edit(id, func(e *Entry) {
		e.State, e.Error = Ready, ""
		e.SongDir = songDir
		if title != "" {
			e.Title = title
		}
	})
}

// RecordScore appends the result of a session to an entry.
func (s *Store) RecordScore(id string, score Score) error {
	score.Players = append([]Player(nil), score.Players...)
	return s.edit(id, func(e *Entry) { e.Scores = append(e.Scores, score) })
}

// ClaimNext takes the first Queued entry, marks it Preparing, and returns it.
// Claiming is one step under the lock so no other caller can claim it too.
func (s *Store) ClaimNext() (Entry, bool) {
	var claimed Entry
	found := false
	s.mutate(func(entries []Entry) ([]Entry, error) {
		for i := range entries {
			if entries[i].State == Queued {
				entries[i].State = Preparing
				entries[i].Error = ""
				claimed, found = entries[i].clone(), true
				return entries, nil
			}
		}
		return entries, nil
	})
	return claimed, found
}

// edit applies fn to one entry and persists.
func (s *Store) edit(id string, fn func(*Entry)) error {
	return s.update(id, func(entries []Entry, i int) []Entry {
		fn(&entries[i])
		return entries
	})
}

// update locates an entry and applies fn to a private copy of the queue.
func (s *Store) update(id string, fn func(entries []Entry, i int) []Entry) error {
	return s.mutate(func(entries []Entry) ([]Entry, error) {
		for i, e := range entries {
			if e.ID == id {
				return fn(entries, i), nil
			}
		}
		return nil, ErrNotFound
	})
}

// mutate runs fn on a copy of the queue and swaps it in only if it was saved.
// A failed write therefore leaves memory and disk agreeing, instead of the
// process believing in a queue the file does not have.
func (s *Store) mutate(fn func(entries []Entry) ([]Entry, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		next[i] = e.clone()
	}
	next, err := fn(next)
	if err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

func (s *Store) save(entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	data, err := json.MarshalIndent(fileFormat{Version: Version, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding queue: %w", err)
	}
	return writeFileAtomic(s.path, append(data, '\n'))
}

// writeFileAtomic replaces path so a crash leaves either the old contents or
// the new, never a torn file: write a temp file beside it (rename is only
// atomic within a filesystem), fsync it, rename it over the target, then
// fsync the directory so the rename itself survives power loss.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("saving queue: %w", err)
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("saving queue: %w", err)
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("saving queue: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving queue: %w", err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving queue: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving queue: %w", err)
	}
	// Directory sync is unsupported on some platforms; the rename already
	// happened, so a failure here is not worth failing the save.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
