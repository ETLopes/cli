package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/karaoke/song"
	"github.com/ETLopes/cli/internal/ui"
)

// Seams the tests replace: how the app is built and how the tools are ensured.
var (
	karaokeAppFor      = newKaraokeApp
	karaokeEnsureTools = ensureKaraokeTools
)

// newKaraokeCmd builds the karaoke tool.
func newKaraokeCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "karaoke [url...]",
		Short:         i18n.T("cmd.karaoke.short"),
		Long:          i18n.T("cmd.karaoke.long"),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, urls []string) error {
			app, err := karaokeSetup(cmd.Context(), e)
			if err != nil {
				return err
			}
			if e.plain || !term.IsTerminal(os.Stdin.Fd()) {
				return runKaraokePlain(cmd.Context(), app, ui.Out, urls)
			}
			return runKaraokeTUI(cmd.Context(), app, urls)
		},
	}
	cmd.AddCommand(newKaraokeDoctorCmd(e), newKaraokeCalibrateCmd(e),
		newKaraokeExportCmd(e), newKaraokeReplayCmd(e))
	return cmd
}

// karaokeSetup makes sure the tools exist, offering to install what it can
// manage, and builds the app around them.
func karaokeSetup(ctx context.Context, e *env) (*karaokeApp, error) {
	tools, err := karaokeEnsureTools(ctx, e)
	if err != nil {
		return nil, err
	}
	app := karaokeAppFor(e)
	if tools != nil {
		app.demucsPath = tools.demucsPath
	}
	return app, nil
}

// runKaraokePlain adds urls to the persisted queue, then prepares everything in
// it in the foreground, one line per stage, until every entry is Ready, Sung or
// Failed.
func runKaraokePlain(ctx context.Context, app *karaokeApp, w io.Writer, urls []string) error {
	store, warn, err := app.OpenQueue()
	if err != nil {
		return err
	}
	if warn != nil {
		fmt.Fprintln(w, ui.Warning(i18n.Tf("karaoke.plain.queue_reset", warn.MovedTo)))
	}

	for _, raw := range urls {
		url := strings.TrimSpace(raw)
		if err := validateURL(url); err != nil {
			fmt.Fprintln(w, ui.Failure(fmt.Sprintf("%s: %v", url, err)))
			continue
		}
		entry, err := store.Add(url)
		var dup *queue.DuplicateError
		switch {
		case errors.As(err, &dup):
			fmt.Fprintln(w, ui.Warning(i18n.Tf("karaoke.plain.duplicate", entryName(dup.Existing), dup.Existing.State)))
		case err != nil:
			return err
		default:
			fmt.Fprintln(w, ui.Success(i18n.Tf("karaoke.plain.queued", entry.URL)))
		}
	}

	if settled(store) {
		fmt.Fprintln(w, ui.Muted.Render(ui.GlyphBullet+" "+i18n.T("karaoke.plain.nothing")))
		return summarize(store)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker := app.NewWorker(store)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = worker.Run(runCtx)
	}()

	last := map[string]song.Stage{}
	for ev := range worker.Events() {
		printEvent(w, store, last, ev)
		if ev.Kind == queue.EventState && settled(store) {
			cancel()
		}
	}
	<-runDone
	if err := ctx.Err(); err != nil {
		return err
	}
	return summarize(store)
}

// settled reports whether nothing in the queue is waiting or running.
func settled(store *queue.Store) bool {
	for _, en := range store.Entries() {
		if en.State == queue.Queued || en.State == queue.Preparing {
			return false
		}
	}
	return true
}

// entryName is what to call an entry before and after it has a title.
func entryName(en queue.Entry) string {
	if en.Title != "" {
		return en.Title
	}
	return en.URL
}

// printEvent turns a worker event into a line: a stage is announced when it
// starts and confirmed when the next one (or the end) replaces it.
func printEvent(w io.Writer, store *queue.Store, last map[string]song.Stage, ev queue.Event) {
	en, _ := store.Get(ev.ID)
	name := entryName(en)
	switch ev.Kind {
	case queue.EventWarning:
		fmt.Fprintln(w, ui.Warning(name+": "+ev.Warning))
	case queue.EventProgress:
		if prev := last[ev.ID]; prev != ev.Stage {
			if prev != "" {
				fmt.Fprintln(w, ui.Success(prev.Label()))
			}
			last[ev.ID] = ev.Stage
			fmt.Fprintln(w, ui.Muted.Render(ui.GlyphBullet+" "+name+": "+ev.Stage.Label()+"..."))
		}
	case queue.EventState:
		if prev := last[ev.ID]; prev != "" && ev.State != queue.Preparing {
			if ev.State != queue.Failed {
				fmt.Fprintln(w, ui.Success(prev.Label()))
			}
			delete(last, ev.ID)
		}
		switch ev.State {
		case queue.Ready:
			fmt.Fprintln(w, ui.Success(i18n.Tf("karaoke.plain.ready", name)))
		case queue.Failed:
			fmt.Fprintln(w, ui.Failure(i18n.Tf("karaoke.plain.failed", name, ev.Err)))
		}
	}
}

// summarize reports how the queue ended, failing when a song did.
func summarize(store *queue.Store) error {
	failed := 0
	for _, en := range store.Entries() {
		if en.State == queue.Failed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s", i18n.Tf("karaoke.plain.failed_n", failed))
	}
	return nil
}

// runKaraokeEntry is what the launcher runs: the interactive screen, with no
// URLs to add.
func runKaraokeEntry(ctx context.Context, e *env) error {
	app, err := karaokeSetup(ctx, e)
	if err != nil {
		return err
	}
	return runKaraokeTUI(ctx, app, nil)
}
