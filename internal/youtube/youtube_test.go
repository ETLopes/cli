package youtube

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
)

func TestInspectParsesMetadata(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: `{"id":"abc123","title":"Ação de Graças","uploader":"Some Channel","duration":212.5,"webpage_url":"https://youtu.be/abc123"}`,
	})

	got, err := New(f).Inspect(context.Background(), "https://youtu.be/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "abc123" || got.Title != "Ação de Graças" || got.Uploader != "Some Channel" {
		t.Errorf("got %+v, want the metadata from the JSON", got)
	}
	if want := 212500 * time.Millisecond; got.Duration != want {
		t.Errorf("Duration = %v, want %v", got.Duration, want)
	}
}

func TestInspectRejectsPlaylists(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: `{"_type":"playlist","id":"PL123","title":"My Playlist"}`,
	})

	_, err := New(f).Inspect(context.Background(), "https://youtube.com/playlist?list=PL123")
	if err == nil {
		t.Fatal("expected a playlist URL to be rejected")
	}
	if !strings.Contains(err.Error(), "playlist") {
		t.Errorf("error = %v, want it to explain the URL is a playlist", err)
	}
}

func TestInspectFallsBackToIDWhenTitleMissing(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", nil, runnertest.Response{Stdout: `{"id":"xyz"}`})

	got, err := New(f).Inspect(context.Background(), "https://youtu.be/xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "xyz" {
		t.Errorf("Title = %q, want the ID as a fallback", got.Title)
	}
}

func TestDownloadRequestsAudioOnly(t *testing.T) {
	dir := t.TempDir()
	f := runnertest.New()
	f.HandleFunc("yt-dlp", nil, func(spec runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(runner.Spec) error {
			return os.WriteFile(filepath.Join(dir, "source.m4a"), []byte("audio"), 0o644)
		}}
	})

	path, err := New(f).Download(context.Background(), "https://youtu.be/x", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "source.m4a" {
		t.Errorf("path = %q, want the downloaded source file", path)
	}

	c := f.CallsTo("yt-dlp")[0]
	if got, _ := c.ArgAfter("--format"); got != "bestaudio/best" {
		t.Errorf("--format = %q, want bestaudio/best", got)
	}
	// A playlist URL would otherwise pull down every video in it.
	if !c.HasArg("--no-playlist") {
		t.Error("expected --no-playlist")
	}
}

// Re-downloading is wasted network time when a previous run already succeeded.
func TestDownloadReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.webm"), []byte("already-here"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := runnertest.New()
	path, err := New(f).Download(context.Background(), "https://youtu.be/x", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "source.webm" {
		t.Errorf("path = %q, want the existing file", path)
	}
	if got := len(f.CallsTo("yt-dlp")); got != 0 {
		t.Errorf("made %d yt-dlp calls, want 0 when the file already exists", got)
	}
}

// A .part file is an interrupted download, not a usable source.
func TestDownloadIgnoresPartialFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.m4a.part"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := runnertest.New()
	f.HandleFunc("yt-dlp", nil, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(runner.Spec) error {
			return os.WriteFile(filepath.Join(dir, "source.m4a"), []byte("complete"), 0o644)
		}}
	})

	path, err := New(f).Download(context.Background(), "https://youtu.be/x", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "source.m4a" {
		t.Errorf("path = %q, want the freshly downloaded file", path)
	}
	if got := len(f.CallsTo("yt-dlp")); got != 1 {
		t.Errorf("made %d yt-dlp calls, want 1", got)
	}
}

func TestDownloadFailsWhenNoFileAppears(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", nil, runnertest.Response{})

	_, err := New(f).Download(context.Background(), "https://youtu.be/x", t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected an error when yt-dlp writes nothing")
	}
}

func TestCookiesFlagPassedThrough(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", nil, runnertest.Response{Stdout: `{"id":"x","title":"t"}`})

	c := New(f)
	c.CookiesFromBrowser = "firefox"
	if _, err := c.Inspect(context.Background(), "https://youtu.be/x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := f.CallsTo("yt-dlp")[0].ArgAfter("--cookies-from-browser"); got != "firefox" {
		t.Errorf("--cookies-from-browser = %q, want firefox", got)
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		line string
		want float64
		ok   bool
	}{
		{"DTXPROG  45.2%", 0.452, true},
		{"DTXPROG 100.0%", 1, true},
		{"[download]  12.3% of 4.00MiB", 0.123, true},
		{"[download] Destination: source.m4a", 0, false},
		{"random noise", 0, false},
	}
	for _, tt := range tests {
		got, ok := parsePercent(tt.line)
		if ok != tt.ok {
			t.Errorf("parsePercent(%q) matched = %v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if ok && (got < tt.want-0.001 || got > tt.want+0.001) {
			t.Errorf("parsePercent(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// YouTube intermittently returns a format list with no audio in it. Metadata
// extraction must survive that: only the download genuinely needs a format.
func TestInspectToleratesMissingFormats(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", []string{"--dump-single-json"}, runnertest.Response{
		Stdout: `{"id":"x","title":"Some Song","duration":293}`,
	})

	if _, err := New(f).Inspect(context.Background(), "https://youtu.be/x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.CallsTo("yt-dlp")[0].HasArg("--ignore-no-formats-error") {
		t.Error("Inspect should not let format selection fail metadata extraction")
	}
}

// The download does need a real format, so it must still fail loudly.
func TestDownloadStillRequiresFormats(t *testing.T) {
	f := runnertest.New()
	f.Handle("yt-dlp", nil, runnertest.Response{
		Err: &runner.ExitError{
			Command: "yt-dlp", ExitCode: 1,
			Output: []string{"ERROR: Requested format is not available"},
		},
	})

	_, err := New(f).Download(context.Background(), "https://youtu.be/x", t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected the download to fail when no format is available")
	}
	if c := f.CallsTo("yt-dlp")[0]; c.HasArg("--ignore-no-formats-error") {
		t.Error("the download must not ignore a missing format")
	}
}
