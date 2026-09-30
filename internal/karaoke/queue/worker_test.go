package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/song"
)

const wait = 5 * time.Second

// fakePreparer stands in for the real pipeline. behave decides what one call
// does; the fake records calls and asserts that none overlap.
type fakePreparer struct {
	behave func(ctx context.Context, url string, observe song.Observer) error

	mu         sync.Mutex
	calls      []string
	active     int
	overlapped bool
	started    chan string
}

func newFake(behave func(ctx context.Context, url string, observe song.Observer) error) *fakePreparer {
	return &fakePreparer{behave: behave, started: make(chan string, 1000)}
}

func (f *fakePreparer) Prepare(ctx context.Context, url, songsDir string, observe song.Observer) (song.Song, error) {
	f.mu.Lock()
	f.calls = append(f.calls, url)
	f.active++
	if f.active > 1 {
		f.overlapped = true
	}
	f.mu.Unlock()
	f.started <- url
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	var err error
	if f.behave != nil {
		err = f.behave(ctx, url, observe)
	}
	if err != nil {
		return song.Song{}, err
	}
	id, _ := song.VideoID(url)
	return song.Song{Dir: filepath.Join(songsDir, "song-"+id), Manifest: song.Manifest{Title: "Title " + id}}, nil
}

func (f *fakePreparer) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// harness runs a worker over a fresh store until the test ends.
type harness struct {
	t      *testing.T
	store  *Store
	path   string
	worker *Worker
	prep   *fakePreparer
	cancel context.CancelFunc
	done   chan error
	events []Event // consumed but unmatched events, in order
}

func start(t *testing.T, behave func(ctx context.Context, url string, observe song.Observer) error, urls ...string) *harness {
	t.Helper()
	s, path := openStore(t)
	for _, u := range urls {
		mustAdd(t, s, u)
	}
	return startOn(t, s, path, newFake(behave))
}

func startOn(t *testing.T, s *Store, path string, prep *fakePreparer) *harness {
	t.Helper()
	w := NewWorker(s, prep, filepath.Join(t.TempDir(), "songs"))
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, store: s, path: path, worker: w, prep: prep, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- w.Run(ctx) }()
	t.Cleanup(func() { h.stop() })
	return h
}

func (h *harness) stop() {
	h.cancel()
	select {
	case <-h.done:
		h.done <- context.Canceled // idempotent: later stop calls see it too
	case <-time.After(wait):
		h.t.Error("worker did not stop")
	}
}

// waitState reads events until id reaches state.
func (h *harness) waitState(id string, state State) Event {
	h.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case ev, ok := <-h.worker.Events():
			if !ok {
				h.t.Fatalf("events closed before %s reached %s", id, state)
			}
			h.events = append(h.events, ev)
			if ev.Kind == EventState && ev.ID == id && ev.State == state {
				return ev
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for %s to become %s", id, state)
		}
	}
}

func (h *harness) waitStarted() string {
	h.t.Helper()
	select {
	case u := <-h.prep.started:
		return u
	case <-time.After(wait):
		h.t.Fatal("timed out waiting for a preparation to start")
		return ""
	}
}

func youtubeURL(n int) string { return fmt.Sprintf("https://youtu.be/id%09d", n) }
func youtubeID(n int) string  { return fmt.Sprintf("id%09d", n) }

func TestPreparesEntriesSeriallyInQueueOrderAndContinuesPastAFailure(t *testing.T) {
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		if url == youtubeURL(2) {
			return &song.StageError{Stage: song.StageSeparate, Err: errors.New("demucs exploded")}
		}
		observe(song.Event{Stage: song.StageDownload, Fraction: 0.5})
		return nil
	}
	h := start(t, behave, youtubeURL(1), youtubeURL(2), youtubeURL(3))

	h.waitState(youtubeID(3), Ready)

	if got, want := h.prep.called(), []string{youtubeURL(1), youtubeURL(2), youtubeURL(3)}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if h.prep.overlapped {
		t.Error("two preparations ran at once")
	}
	e1, _ := h.store.Get(youtubeID(1))
	e2, _ := h.store.Get(youtubeID(2))
	if e1.State != Ready || e1.SongDir == "" || e1.Title != "Title "+youtubeID(1) {
		t.Errorf("first entry = %+v", e1)
	}
	if e2.State != Failed || e2.Stage != song.StageSeparate || e2.Error == "" {
		t.Errorf("second entry = %+v", e2)
	}
}

func TestPublishesStateChangesInOrderAndForwardsProgressAndWarnings(t *testing.T) {
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		observe(song.Event{Stage: song.StageInspect, Detail: "Real Title", Fraction: 1, Done: true})
		observe(song.Event{Stage: song.StageLyrics, Warning: "lyrics unavailable"})
		return nil
	}
	h := start(t, behave, youtubeURL(1))
	h.waitState(youtubeID(1), Ready)

	var kinds []EventKind
	var states []State
	var warning string
	for _, ev := range h.events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == EventState {
			states = append(states, ev.State)
		}
		if ev.Kind == EventWarning {
			warning = ev.Warning
		}
	}
	if !reflect.DeepEqual(states, []State{Preparing, Ready}) {
		t.Errorf("states = %v", states)
	}
	if warning != "lyrics unavailable" {
		t.Errorf("warning = %q (kinds %v)", warning, kinds)
	}
	sawProgress := false
	for _, ev := range h.events {
		if ev.Kind == EventProgress && ev.Stage == song.StageInspect {
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Error("no progress event forwarded")
	}
	// Title arrives from the inspect stage; Finish then confirms it from the manifest.
	if e, _ := h.store.Get(youtubeID(1)); e.Stage != song.StageLyrics {
		t.Errorf("stage = %q, want lyrics", e.Stage)
	}
}

func TestATitleFromTheInspectStageShowsBeforeThePreparationFinishes(t *testing.T) {
	release := make(chan struct{})
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		observe(song.Event{Stage: song.StageInspect, Detail: "Early Title", Fraction: 1, Done: true})
		observe(song.Event{Stage: song.StageDownload}) // an event after the title has been stored
		<-release
		return nil
	}
	h := start(t, behave, youtubeURL(1))
	h.waitStarted()
	deadline := time.After(wait)
	for {
		if e, _ := h.store.Get(youtubeID(1)); e.Title == "Early Title" && e.Stage == song.StageDownload {
			break
		}
		select {
		case <-deadline:
			t.Fatal("title never appeared while preparing")
		case <-time.After(time.Millisecond):
		}
	}
	close(release)
}

func TestPausePreventsNewSongsFromStartingUntilResumed(t *testing.T) {
	s, path := openStore(t)
	prep := newFake(nil)
	h := startOn(t, s, path, prep)
	h.worker.Pause()

	mustAdd(t, s, youtubeURL(1))
	select {
	case u := <-prep.started:
		t.Fatalf("%s started while paused", u)
	case <-time.After(50 * time.Millisecond):
	}

	h.worker.Resume()
	h.waitState(youtubeID(1), Ready)
}

func TestPauseDoesNotCancelARunningSong(t *testing.T) {
	release := make(chan struct{})
	h := start(t, func(ctx context.Context, url string, observe song.Observer) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, youtubeURL(1))
	h.waitStarted()
	h.worker.Pause()
	close(release)
	h.waitState(youtubeID(1), Ready)
}

func TestRemovingTheEntryBeingPreparedCancelsItAndMovesOn(t *testing.T) {
	var firstCancelled sync.WaitGroup
	firstCancelled.Add(1)
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		if url == youtubeURL(1) {
			<-ctx.Done()
			firstCancelled.Done()
			return ctx.Err()
		}
		return nil
	}
	h := start(t, behave, youtubeURL(1), youtubeURL(2))
	if got := h.waitStarted(); got != youtubeURL(1) {
		t.Fatalf("started %s first", got)
	}

	if err := h.worker.Remove(youtubeID(1)); err != nil {
		t.Fatal(err)
	}
	firstCancelled.Wait()
	h.waitState(youtubeID(2), Ready)

	if _, ok := h.store.Get(youtubeID(1)); ok {
		t.Error("removed entry is still in the store")
	}
}

func TestAnEntryRemovedFromTheStoreDirectlyStopsItsJobOnTheNextProgressEvent(t *testing.T) {
	s, path := openStore(t)
	mustAdd(t, s, youtubeURL(1))
	mustAdd(t, s, youtubeURL(2))
	stopped := make(chan struct{})
	prep := newFake(func(ctx context.Context, url string, observe song.Observer) error {
		if url != youtubeURL(1) {
			return nil
		}
		s.Remove(youtubeID(1))
		observe(song.Event{Stage: song.StageDownload, Fraction: 0.1})
		select {
		case <-ctx.Done():
			close(stopped)
			return ctx.Err()
		case <-time.After(wait):
			return errors.New("job was not cancelled")
		}
	})
	h := startOn(t, s, path, prep)
	<-stopped
	h.waitState(youtubeID(2), Ready)
}

func TestCancelCurrentMarksTheEntryFailedAsCancelledAndMovesOn(t *testing.T) {
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		if url == youtubeURL(1) {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	h := start(t, behave, youtubeURL(1), youtubeURL(2))
	h.waitStarted()
	h.worker.CancelCurrent()
	h.waitState(youtubeID(1), Failed)
	h.waitState(youtubeID(2), Ready)
	if e, _ := h.store.Get(youtubeID(1)); e.Error != "cancelled" {
		t.Errorf("error = %q, want cancelled", e.Error)
	}
}

func TestCancellingRunMidPreparationLeavesTheEntryQueuedWithItsStageKept(t *testing.T) {
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		observe(song.Event{Stage: song.StageSeparate, Fraction: 0.4})
		<-ctx.Done()
		return &song.StageError{Stage: song.StageSeparate, Err: ctx.Err()}
	}
	h := start(t, behave, youtubeURL(1))
	h.waitStarted()
	h.stop()

	for _, e := range reopen(t, h.path).Entries() {
		if e.State != Queued || e.Error != "" || e.Stage != song.StageSeparate {
			t.Errorf("entry on disk = %+v, want queued at separate with no error", e)
		}
	}
	// Events closes once Run has returned, so a consumer's range loop ends.
	timeout := time.After(wait)
	for {
		select {
		case _, ok := <-h.worker.Events():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("events channel was not closed after Run returned")
		}
	}
}

func TestConcurrentAddsWhileTheWorkerIsBusyAppendWithoutDisturbingTheRunningJob(t *testing.T) {
	release := make(chan struct{})
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		if url == youtubeURL(0) {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	h := start(t, behave, youtubeURL(0))
	h.waitStarted()

	const adders, perAdder = 4, 5
	var wg sync.WaitGroup
	for a := 0; a < adders; a++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perAdder; i++ {
				if _, err := h.store.Add(youtubeURL(1 + a*perAdder + i)); err != nil {
					t.Error(err)
				}
				h.store.Entries()
			}
		}()
	}
	wg.Wait()

	if e, _ := h.store.Get(youtubeID(0)); e.State != Preparing {
		t.Fatalf("running job disturbed: %+v", e)
	}
	close(release)
	last := len(h.store.Entries()) - 1
	h.waitState(h.store.Entries()[last].ID, Ready)

	if n := len(h.prep.called()); n != adders*perAdder+1 {
		t.Errorf("prepared %d songs, want %d", n, adders*perAdder+1)
	}
	if h.prep.overlapped {
		t.Error("two preparations ran at once")
	}
	for _, e := range h.store.Entries() {
		if e.State != Ready {
			t.Errorf("%s = %s, want ready", e.ID, e.State)
		}
	}
}

func TestASlowEventConsumerNeverBlocksTheWorkerAndStateChangesSurvive(t *testing.T) {
	const progressPerSong = 500
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		for i := 0; i < progressPerSong; i++ {
			observe(song.Event{Stage: song.StageSeparate, Fraction: float64(i) / progressPerSong})
		}
		return nil
	}
	h := start(t, behave, youtubeURL(1), youtubeURL(2), youtubeURL(3))

	// Nobody reads Events() while all three songs prepare.
	deadline := time.After(wait)
	for {
		ready := 0
		for _, e := range h.store.Entries() {
			if e.State == Ready {
				ready++
			}
		}
		if ready == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker stalled behind an unread event channel")
		case <-time.After(time.Millisecond):
		}
	}

	h.waitState(youtubeID(3), Ready)
	var states []string
	progress := 0
	for _, ev := range h.events {
		switch ev.Kind {
		case EventState:
			states = append(states, ev.ID+":"+string(ev.State))
		case EventProgress:
			progress++
		}
	}
	want := []string{
		youtubeID(1) + ":preparing", youtubeID(1) + ":ready",
		youtubeID(2) + ":preparing", youtubeID(2) + ":ready",
		youtubeID(3) + ":preparing", youtubeID(3) + ":ready",
	}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("state changes = %v, want %v", states, want)
	}
	if progress >= 3*progressPerSong {
		t.Errorf("no progress events were dropped (%d)", progress)
	}
}

func TestRetryingAFailedEntryMakesTheWorkerPickItUpAgain(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	behave := func(ctx context.Context, url string, observe song.Observer) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return errors.New("network down")
		}
		return nil
	}
	h := start(t, behave, youtubeURL(1))
	h.waitState(youtubeID(1), Failed)

	if err := h.store.SetState(youtubeID(1), Queued); err != nil {
		t.Fatal(err)
	}
	h.waitState(youtubeID(1), Ready)
	if e, _ := h.store.Get(youtubeID(1)); e.Error != "" {
		t.Errorf("error not cleared after a successful retry: %q", e.Error)
	}
}

func TestAnEntryLeftPreparingByACrashIsResumedByTheWorkerAfterReopen(t *testing.T) {
	s, path := openStore(t)
	mustAdd(t, s, youtubeURL(1))
	if _, ok := s.ClaimNext(); !ok { // the process dies here
		t.Fatal("nothing claimed")
	}

	h := startOn(t, reopen(t, path), path, newFake(nil))
	h.waitState(youtubeID(1), Ready)
	if got := h.prep.called(); len(got) != 1 {
		t.Errorf("calls = %v, want one resumed preparation", got)
	}
}

func TestRunCannotBeCalledTwice(t *testing.T) {
	s, _ := openStore(t)
	w := NewWorker(s, newFake(nil), t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run = %v, want context.Canceled", err)
	}
	if err := w.Run(ctx); err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("second Run = %v, want an already-run error", err)
	}
}
