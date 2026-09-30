package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/karaoke/queue"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
)

const (
	kTestID    = "abc123DEF45"
	kTestURL   = "https://www.youtube.com/watch?v=" + kTestID
	kTestTitle = "Placeholder Artist - Placeholder Title"
	kContour   = `{"hop":0.016,"sample_rate":16000,"time":[0,0.016],"hz":[220,220],"confidence":[0.9,0.9],"loudness_db":[-20,-20]}`
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// prepRunner fakes yt-dlp, demucs, ffmpeg and the pitch tracker, writing the
// files each would have, so a song prepares with nothing installed.
func prepRunner() *runnertest.Fake {
	f := runnertest.New()
	touch := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: fmt.Sprintf(`{"id":%q,"title":%q,"uploader":"Some Channel","duration":200,"webpage_url":%q}`, kTestID, kTestTitle, kTestURL),
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
	last := func(content string) func(runner.Spec) runnertest.Response {
		return func(runner.Spec) runnertest.Response {
			return runnertest.Response{Do: func(spec runner.Spec) error {
				return touch(spec.Args[len(spec.Args)-1], content)
			}}
		}
	}
	f.HandleFunc("ffmpeg", nil, last("RIFF"))
	f.HandleFunc("venv-python", nil, last(kContour))
	return f
}

// prepApp is an app whose preparer runs against fakes.
func prepApp(t *testing.T) (*karaokeApp, *runnertest.Fake) {
	t.Helper()
	app, _ := fxApp(t)
	f := prepRunner()
	app.run = f
	app.python = "venv-python"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			fmt.Fprintf(w, `{"id":1,"duration":200,"instrumental":false,"syncedLyrics":%q}`, "[00:01.00]la la la\n[00:03.00]placeholder words\n")
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	app.lyricsURL = srv.URL + "/api"
	return app, f
}

func TestPlainModeQueuesPreparesAndPrintsALinePerStage(t *testing.T) {
	app, _ := prepApp(t)
	var out bytes.Buffer
	if err := runKaraokePlain(context.Background(), app, &out, []string{kTestURL}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := plain(out.String())
	for _, want := range []string{"queued " + kTestURL, "Downloading audio", "Isolating the vocals", "Tracing the original melody", "ready: " + kTestTitle} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	store, _, err := app.OpenQueue()
	fxMust(t, err)
	entries := store.Entries()
	if len(entries) != 1 || entries[0].State != queue.Ready || entries[0].SongDir == "" {
		t.Fatalf("queue = %+v, want one ready entry", entries)
	}
}

func TestPlainModeReportsADuplicateAndDoesNotPrepareItAgain(t *testing.T) {
	app, f := prepApp(t)
	var first bytes.Buffer
	fxMust(t, runKaraokePlain(context.Background(), app, &first, []string{kTestURL}))
	calls := len(f.Calls())

	var out bytes.Buffer
	fxMust(t, runKaraokePlain(context.Background(), app, &out, []string{kTestURL, "not a url"}))
	text := plain(out.String())
	if !strings.Contains(text, "already in the queue: "+kTestTitle+" (ready)") {
		t.Errorf("no duplicate notice:\n%s", text)
	}
	if !strings.Contains(text, "not a url") || !strings.Contains(text, "nothing to prepare") {
		t.Errorf("invalid URL or idle notice missing:\n%s", text)
	}
	if len(f.Calls()) != calls {
		t.Errorf("a duplicate ran %d more commands", len(f.Calls())-calls)
	}
}

func TestPlainModeFailsWhenASongFails(t *testing.T) {
	app, _ := prepApp(t)
	bad := runnertest.New()
	bad.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{Err: fmt.Errorf("boom")})
	app.run = bad
	var out bytes.Buffer
	err := runKaraokePlain(context.Background(), app, &out, []string{kTestURL})
	if err == nil || !strings.Contains(err.Error(), "1 song(s) failed") {
		t.Fatalf("err = %v, want a failed-songs error\n%s", err, out.String())
	}
	if !strings.Contains(plain(out.String()), "failed:") {
		t.Errorf("no failure line:\n%s", out.String())
	}
}

func TestKaraokeCommandRunsPlainThroughTheRoot(t *testing.T) {
	app, _ := prepApp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	karaokeAppFor, karaokeEnsureTools = func(*env) *karaokeApp { return app }, func(context.Context, *env) (*toolset, error) { return &toolset{}, nil }
	t.Cleanup(func() { karaokeAppFor, karaokeEnsureTools = newKaraokeApp, ensureKaraokeTools })

	e := &env{v: viper.New()}
	root := newRootCmd(e)
	root.SetArgs([]string{"karaoke", "--plain", kTestURL})
	fxMust(t, root.ExecuteContext(context.Background()))

	store, _, err := app.OpenQueue()
	fxMust(t, err)
	if es := store.Entries(); len(es) != 1 || es[0].State != queue.Ready {
		t.Fatalf("queue = %+v", es)
	}
}
