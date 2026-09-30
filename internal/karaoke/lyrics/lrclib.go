package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public LRCLIB API.
const DefaultBaseURL = "https://lrclib.net/api"

// durationWindow is how far a candidate's length may differ from the video's
// and still be the same recording. LRCLIB itself allows two seconds on /get;
// beyond that a match is usually a live cut, a remaster or a radio edit whose
// timestamps would drift against the audio we actually play.
const durationWindow = 2 * time.Second

// Client talks to LRCLIB. The zero value works against the public API.
type Client struct {
	// BaseURL overrides DefaultBaseURL, which is how tests point it at an
	// httptest.Server.
	BaseURL string
	// HTTP overrides the transport. Nil uses a client with a modest timeout,
	// because a hung lyrics lookup must not stall song preparation.
	HTTP *http.Client
	// Version is the CLI version, reported in the User-Agent. LRCLIB asks
	// clients to identify themselves so they can be contacted about misuse.
	Version string
}

// Track is one LRCLIB entry.
type Track struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"` // seconds
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// HasSynced reports whether the entry carries timed lyrics.
func (t Track) HasSynced() bool {
	return !t.Instrumental && strings.TrimSpace(t.SyncedLyrics) != ""
}

// HasLyrics reports whether the entry carries any lyric text at all. An
// instrumental flag wins over stray text: LRCLIB marks those deliberately.
func (t Track) HasLyrics() bool {
	return t.HasSynced() || (!t.Instrumental && strings.TrimSpace(t.PlainLyrics) != "")
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) userAgent() string {
	v := c.Version
	if v == "" {
		v = "dev"
	}
	return "cli-karaoke/" + v + " (https://github.com/ETLopes/cli)"
}

// Find looks for lyrics for a recording. ok is false, with a nil error, when
// LRCLIB simply has nothing suitable; err is reserved for the lookup itself
// failing, so a caller can treat "no lyrics" and "API down" differently.
//
// It asks /get first, which is an exact match on title, artist and length, and
// only searches when that is a 404. The search is done twice if needed: once by
// fields and once as free text, since video titles are often too messy for the
// structured query.
func (c *Client) Find(ctx context.Context, track, artist string, duration time.Duration) (Track, bool, error) {
	q := url.Values{}
	q.Set("track_name", track)
	q.Set("artist_name", artist)
	if duration > 0 {
		q.Set("duration", strconv.Itoa(int(math.Round(duration.Seconds()))))
	}

	var exact Track
	status, err := c.getJSON(ctx, "/get", q, &exact)
	if err != nil {
		return Track{}, false, err
	}
	if status == http.StatusOK {
		return exact, true, nil
	}

	for _, sq := range []url.Values{
		{"track_name": {track}, "artist_name": {artist}},
		{"q": {strings.TrimSpace(artist + " " + track)}},
	} {
		var candidates []Track
		status, err := c.getJSON(ctx, "/search", sq, &candidates)
		if err != nil {
			return Track{}, false, err
		}
		if status != http.StatusOK {
			continue
		}
		if best, ok := pickCandidate(candidates, duration); ok {
			return best, true, nil
		}
	}
	return Track{}, false, nil
}

// getJSON performs a GET and decodes a 200 body into out. A 404 is returned as
// a status rather than an error because it is LRCLIB's normal "no such track".
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("lrclib: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("lrclib: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, fmt.Errorf("lrclib: %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("lrclib: reading %s response: %w", path, err)
	}
	return resp.StatusCode, nil
}

// pickCandidate chooses among search results: only those within the duration
// window qualify; among them synced lyrics beat plain, and then the closest
// length wins. With no known duration every candidate qualifies.
func pickCandidate(candidates []Track, duration time.Duration) (Track, bool) {
	var (
		best     Track
		bestDiff = math.MaxFloat64
		found    bool
	)
	for _, cand := range candidates {
		diff := 0.0
		if duration > 0 {
			diff = math.Abs(cand.Duration - duration.Seconds())
			if diff > durationWindow.Seconds() {
				continue
			}
		}
		better := !found ||
			(cand.HasSynced() && !best.HasSynced()) ||
			(cand.HasSynced() == best.HasSynced() && diff < bestDiff)
		if better {
			best, bestDiff, found = cand, diff, true
		}
	}
	return best, found
}

// noiseSuffix matches a trailing bracketed tag that names the kind of upload
// rather than the song.
var noiseSuffix = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:official\s+(?:music\s+)?(?:video|audio|lyric\s+video|visuali[sz]er)|lyrics?(?:\s+video)?|lyric\s+video|audio|visuali[sz]er|music\s+video|hd|hq|4k)\s*[)\]]\s*$`)

// CleanTitle derives the track and artist to search for from a video's title
// and uploader. Titles conventionally read "Artist - Title (Official Video)";
// when there is no "Artist - " prefix the uploader is the best guess, minus the
// " - Topic" suffix YouTube gives auto-generated artist channels.
func CleanTitle(title, uploader string) (track, artist string) {
	title = strings.TrimSpace(title)
	// Strip repeatedly: "(Official Video) [HD]" carries two tags.
	for {
		stripped := strings.TrimSpace(noiseSuffix.ReplaceAllString(title, ""))
		if stripped == title {
			break
		}
		title = stripped
	}

	artist = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(uploader), " - Topic"))
	// Only the first separator splits: titles may contain dashes of their own.
	for _, sep := range []string{" - ", " – ", " — "} {
		if a, t, ok := strings.Cut(title, sep); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t), strings.TrimSpace(a)
		}
	}
	return title, artist
}
