// Package youtube fetches source audio from YouTube (and anything else yt-dlp
// supports) and reports what it found before committing to a download.
package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eduardolopes/dtx/internal/runner"
)

// Tool is the executable this package drives.
const Tool = "yt-dlp"

// sourceStem is the base name every download is written under. A fixed stem
// keeps the on-disk layout predictable regardless of the video's title, which
// may contain characters that are awkward in a path.
const sourceStem = "source"

// Client downloads audio via yt-dlp.
type Client struct {
	Run runner.Runner
	// Path overrides the yt-dlp executable. Empty resolves "yt-dlp" on PATH.
	Path string
	// CookiesFromBrowser, when set (e.g. "chrome", "firefox"), tells yt-dlp to
	// load cookies from that browser. Needed for age-restricted videos and to
	// get past "confirm you're not a bot" interruptions.
	CookiesFromBrowser string
}

// New returns a Client backed by r.
func New(r runner.Runner) *Client { return &Client{Run: r} }

func (c *Client) bin() string {
	if c.Path != "" {
		return c.Path
	}
	return Tool
}

// Info describes a video without downloading it.
type Info struct {
	ID       string
	Title    string
	Uploader string
	Duration time.Duration
	URL      string
}

// ProgressFunc reports fractional download completion in [0,1].
type ProgressFunc func(fraction float64)

// Inspect resolves metadata for url. It is a cheap, network-only call, which
// lets the caller show a title and duration -- and refuse an absurdly long
// video -- before spending time on a download.
func (c *Client) Inspect(ctx context.Context, url string) (Info, error) {
	res, err := c.Run.Run(ctx, runner.Spec{
		Name: c.bin(),
		// Metadata does not depend on which media formats are on offer, but
		// yt-dlp still runs format selection and fails the whole call when it
		// comes up empty -- which YouTube does intermittently under throttling.
		// Ignoring that keeps the title and duration readable regardless; the
		// download stage still fails loudly if no audio is really available.
		Args: c.args("--dump-single-json", "--no-warnings", "--ignore-no-formats-error", url),
	})
	if err != nil {
		return Info{}, fmt.Errorf("reading video info: %w", err)
	}

	var meta struct {
		ID         string          `json:"id"`
		Title      string          `json:"title"`
		Uploader   string          `json:"uploader"`
		Duration   json.RawMessage `json:"duration"`
		WebpageURL string          `json:"webpage_url"`
		Type       string          `json:"_type"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &meta); err != nil {
		return Info{}, fmt.Errorf("reading video info: parsing yt-dlp output: %w", err)
	}
	if meta.Type == "playlist" {
		return Info{}, fmt.Errorf("that URL is a playlist; pass a single video URL")
	}

	info := Info{ID: meta.ID, Title: meta.Title, Uploader: meta.Uploader, URL: meta.WebpageURL}
	if info.URL == "" {
		info.URL = url
	}
	if secs, err := strconv.ParseFloat(strings.Trim(string(meta.Duration), `"`), 64); err == nil && secs > 0 {
		info.Duration = time.Duration(secs * float64(time.Second))
	}
	if info.Title == "" {
		info.Title = info.ID
	}
	return info, nil
}

// Download fetches the best available audio-only stream into dir and returns
// the path to the downloaded file. The container is whatever the site serves
// (usually .m4a or .webm); converting it is the audio package's job.
//
// An existing download in dir is reused rather than re-fetched, so re-running
// the pipeline after a later stage failed does not repeat the network work.
func (c *Client) Download(ctx context.Context, url, dir string, onProgress ProgressFunc) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating download directory: %w", err)
	}
	if existing, ok := findExisting(dir); ok {
		if onProgress != nil {
			onProgress(1)
		}
		return existing, nil
	}

	var onLine runner.LineFunc
	if onProgress != nil {
		onLine = func(_ runner.Stream, line string) {
			if f, ok := parsePercent(line); ok {
				onProgress(f)
			}
		}
	}

	_, err := c.Run.Run(ctx, runner.Spec{
		Name: c.bin(),
		Args: c.args(
			"--format", "bestaudio/best",
			"--newline",
			"--progress-template", "DTXPROG %(progress._percent_str)s",
			"--output", filepath.Join(dir, sourceStem+".%(ext)s"),
			url,
		),
		OnLine: onLine,
	})
	if err != nil {
		return "", fmt.Errorf("downloading audio: %w", err)
	}

	path, ok := findExisting(dir)
	if !ok {
		return "", fmt.Errorf("downloading audio: yt-dlp reported success but no file appeared in %s", dir)
	}
	if onProgress != nil {
		onProgress(1)
	}
	return path, nil
}

// args prepends the flags every invocation shares.
func (c *Client) args(rest ...string) []string {
	out := []string{"--no-playlist"}
	if c.CookiesFromBrowser != "" {
		out = append(out, "--cookies-from-browser", c.CookiesFromBrowser)
	}
	return append(out, rest...)
}

// findExisting locates a previously downloaded source file in dir.
func findExisting(dir string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(dir, sourceStem+".*"))
	if err != nil {
		return "", false
	}
	for _, m := range matches {
		// yt-dlp writes .part files while downloading; those are not usable.
		if strings.HasSuffix(m, ".part") || strings.HasSuffix(m, ".ytdl") {
			continue
		}
		if info, err := os.Stat(m); err == nil && info.Size() > 0 {
			return m, true
		}
	}
	return "", false
}

// percentPattern matches both our custom progress template and yt-dlp's
// default "[download]  12.3% of ..." line, so progress still works if the
// template flag is unsupported by an older yt-dlp.
var percentPattern = regexp.MustCompile(`(?:DTXPROG|\[download\])\s+([0-9]{1,3}(?:\.[0-9]+)?)%`)

func parsePercent(line string) (float64, bool) {
	m := percentPattern.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return min(v/100, 1), true
}
