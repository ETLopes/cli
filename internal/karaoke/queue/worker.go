package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/ETLopes/cli/internal/karaoke/song"
)

// Preparer is the part of *song.Preparer the worker uses, so tests can stand
// in for the real download-separate-render pipeline.
type Preparer interface {
	Prepare(ctx context.Context, url, songsDir string, observe song.Observer) (song.Song, error)
}

var _ Preparer = (*song.Preparer)(nil)

// EventKind says what an Event reports.
type EventKind int

const (
	// EventState reports an entry changing State. Never dropped.
	EventState EventKind = iota
	// EventProgress reports stage progress. Superseded by newer progress for
	// the same entry when the consumer is slow.
	EventProgress
	// EventWarning reports a non-fatal problem, such as a lyrics lookup that
	// failed. Never dropped.
	EventWarning
)

// Event is what the worker tells a UI. Which fields are set depends on Kind.
type Event struct {
	ID   string
	Kind EventKind

	// State is the new state (EventState).
	State State
	// Err is the failure message when State is Failed.
	Err string

	// Stage, Fraction and Detail describe progress (EventProgress).
	Stage    song.Stage
	Fraction float64
	Detail   string

	// Warning is the message (EventWarning).
	Warning string
}

// eventBuffer is the capacity of the events channel handed to the consumer.
const eventBuffer = 64

// Removal and manual cancellation reach the job as its context's cause, which
// is how the worker tells them apart from shutdown when Prepare returns.
var (
	errRemoved   = errors.New("entry removed")
	errCancelled = errors.New("cancelled")
)

// Worker prepares queued songs one at a time in the background.
type Worker struct {
	store    *Store
	prep     Preparer
	songsDir string

	events chan Event
	out    outbox

	ran atomic.Bool

	mu      sync.Mutex
	paused  bool
	current *job
	// kick wakes the loop when Resume lifts a pause. Sticky (capacity one) for
	// the same reason as Store.WorkAvailable.
	kick chan struct{}
}

type job struct {
	id     string
	cancel context.CancelCauseFunc
}

// NewWorker returns a worker that prepares songs from store into songsDir.
func NewWorker(store *Store, prep Preparer, songsDir string) *Worker {
	return &Worker{
		store:    store,
		prep:     prep,
		songsDir: songsDir,
		events:   make(chan Event, eventBuffer),
		out:      outbox{wake: make(chan struct{}, 1)},
		kick:     make(chan struct{}, 1),
	}
}

// Events is the stream of what the worker is doing. It is closed when Run
// returns.
//
// The worker never blocks on this channel, however slow the reader. Events go
// through an internal outbox drained by a separate goroutine, so a stalled UI
// stalls only that goroutine. The outbox keeps every state change and warning
// (there are a handful per song), but collapses progress: if a newer progress
// event for the same entry arrives while the previous one is still undelivered,
// the older one is replaced. A reader that catches up therefore sees every
// state change in order and, per entry, only the latest progress.
//
// When Run stops, events the reader has not taken and the channel cannot hold
// are discarded rather than waiting on it.
func (w *Worker) Events() <-chan Event { return w.events }

// Pause stops the worker from starting new songs, for example while one is
// being sung and the CPU belongs to the audio. A song already being prepared
// carries on.
func (w *Worker) Pause() {
	w.mu.Lock()
	w.paused = true
	w.mu.Unlock()
}

// Resume lets the worker start songs again.
func (w *Worker) Resume() {
	w.mu.Lock()
	w.paused = false
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// CancelCurrent aborts the song being prepared, if any. Its entry is marked
// Failed as cancelled; use Remove to also drop it from the queue.
func (w *Worker) CancelCurrent() { w.cancelJob("", errCancelled) }

// Remove deletes an entry from the queue, cancelling its preparation first if
// it is the one running, so the worker moves on to the next song.
func (w *Worker) Remove(id string) error {
	if err := w.store.Remove(id); err != nil {
		return err
	}
	w.cancelJob(id, errRemoved)
	return nil
}

// cancelJob cancels the running job, if it is id (or any job when id is empty).
func (w *Worker) cancelJob(id string, cause error) {
	w.mu.Lock()
	j := w.current
	w.mu.Unlock()
	if j != nil && (id == "" || j.id == id) {
		j.cancel(cause)
	}
}

func (w *Worker) isPaused() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.paused
}

// Run prepares songs until ctx is cancelled, then returns ctx.Err(). Cancelling
// mid-song puts that entry back to Queued: the interruption is not the song's
// fault, and the manifest keeps the stages it finished. Run may be called once.
func (w *Worker) Run(ctx context.Context) error {
	if !w.ran.CompareAndSwap(false, true) {
		return errors.New("worker has already run")
	}
	done := make(chan struct{})
	pumped := make(chan struct{})
	go w.pump(done, pumped)
	defer func() {
		close(done)
		<-pumped
	}()

	for ctx.Err() == nil {
		if !w.isPaused() {
			if e, ok := w.store.ClaimNext(); ok {
				w.prepare(ctx, e)
				continue
			}
		}
		select {
		case <-ctx.Done():
		case <-w.store.WorkAvailable():
		case <-w.kick:
		}
	}
	return ctx.Err()
}

// prepare runs one claimed entry to a resting state.
func (w *Worker) prepare(ctx context.Context, e Entry) {
	jctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	w.mu.Lock()
	w.current = &job{id: e.ID, cancel: cancel}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.current = nil
		w.mu.Unlock()
	}()

	w.publish(Event{ID: e.ID, Kind: EventState, State: Preparing})

	observe := func(ev song.Event) {
		// A store that no longer has the entry means it was removed behind the
		// worker's back; there is nothing left to prepare it for.
		if err := w.store.SetStage(e.ID, ev.Stage); errors.Is(err, ErrNotFound) {
			cancel(errRemoved)
			return
		}
		if ev.Stage == song.StageInspect && ev.Done && ev.Detail != "" {
			w.store.SetTitle(e.ID, ev.Detail)
		}
		if ev.Warning != "" {
			w.publish(Event{ID: e.ID, Kind: EventWarning, Stage: ev.Stage, Warning: ev.Warning})
		}
		w.publish(Event{ID: e.ID, Kind: EventProgress, Stage: ev.Stage, Fraction: ev.Fraction, Detail: ev.Detail})
	}

	s, err := w.prep.Prepare(jctx, e.URL, w.songsDir, observe)

	switch {
	case err == nil:
		if w.store.Finish(e.ID, s.Dir, s.Manifest.Title) == nil {
			w.publish(Event{ID: e.ID, Kind: EventState, State: Ready})
		}
	case ctx.Err() != nil:
		// Shutdown, not failure.
		if w.store.SetState(e.ID, Queued) == nil {
			w.publish(Event{ID: e.ID, Kind: EventState, State: Queued})
		}
	case errors.Is(context.Cause(jctx), errRemoved):
		// The entry is gone; nothing to record.
	default:
		if context.Cause(jctx) == errCancelled {
			err = errCancelled
		}
		if w.store.Fail(e.ID, err) == nil {
			w.publish(Event{ID: e.ID, Kind: EventState, State: Failed, Err: err.Error()})
		}
	}
}

// outbox holds events between the worker and the reader. See Worker.Events.
type outbox struct {
	mu      sync.Mutex
	pending []Event
	wake    chan struct{}
}

func (w *Worker) publish(ev Event) {
	w.out.mu.Lock()
	p := w.out.pending
	if last := len(p) - 1; ev.Kind == EventProgress && last >= 0 &&
		p[last].Kind == EventProgress && p[last].ID == ev.ID {
		p[last] = ev
	} else {
		w.out.pending = append(p, ev)
	}
	w.out.mu.Unlock()
	select {
	case w.out.wake <- struct{}{}:
	default:
	}
}

func (o *outbox) pop() (Event, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pending) == 0 {
		return Event{}, false
	}
	ev := o.pending[0]
	o.pending = o.pending[1:]
	return ev, true
}

// pump moves events from the outbox to the reader's channel. It is the only
// goroutine that ever blocks on the reader.
func (w *Worker) pump(done <-chan struct{}, exited chan<- struct{}) {
	defer close(exited)
	defer close(w.events)
	for {
		ev, ok := w.out.pop()
		if !ok {
			select {
			case <-w.out.wake:
				continue
			case <-done:
				w.flush()
				return
			}
		}
		select {
		case w.events <- ev:
		case <-done:
			// Put it back at the front so the flush keeps order.
			w.out.mu.Lock()
			w.out.pending = append([]Event{ev}, w.out.pending...)
			w.out.mu.Unlock()
			w.flush()
			return
		}
	}
}

// flush delivers what the channel has room for and drops the rest.
func (w *Worker) flush() {
	for {
		ev, ok := w.out.pop()
		if !ok {
			return
		}
		select {
		case w.events <- ev:
		default:
			return
		}
	}
}
