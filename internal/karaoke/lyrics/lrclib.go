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
// searches when that is a 404 or an entry without timed lyrics: LRCLIB often
// holds several uploads of one recording, and /get may pick a plain one while
// a synced one sits a second away. A plain exact match is kept as the
// fallback. The search is done twice if needed: once by fields and once as
// free text, since video titles are often too messy for the structured query.
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
	// An instrumental flag is deliberate, so it ends the lookup like lyrics do.
	if status == http.StatusOK && (exact.HasSynced() || exact.Instrumental) {
		return exact, true, nil
	}
	plainExact := status == http.StatusOK && exact.HasLyrics()

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
		if best, ok := pickCandidate(candidates, duration); ok && (best.HasSynced() || !plainExact) {
			return best, true, nil
		}
	}
	if status == http.StatusOK {
		return exact, true, nil
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

// bracketed matches one parenthesised or bracketed group, wherever it sits.
var bracketed = regexp.MustCompile(`\s*[(\[【]([^)\]】]*)[)\]】]`)

// splitSegments splits a video title into its "Artist - Title - tag"
// segments. A separator is a dash or bar standing alone between words, so
// "Jay-Z" stays whole and dropped tags leave no empty segment behind.
func splitSegments(title string) []string {
	var segments []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			segments = append(segments, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, word := range strings.Fields(title) {
		switch word {
		case "-", "–", "—", "|":
			flush()
		default:
			cur = append(cur, word)
		}
	}
	flush()
	return segments
}

// uploadMarkers are words that describe the upload rather than the song, in
// English and Portuguese: "(Official Video)", "(COM LETRA NA DESCRIÇÃO)",
// "- Legendas -", "(CC)".
var uploadMarkers = map[string]bool{
	"official": true, "oficial": true, "video": true, "vídeo": true, "videoclipe": true,
	"clipe": true, "clip": true, "music": true, "audio": true, "áudio": true,
	"lyric": true, "lyrics": true, "letra": true, "letras": true,
	"legenda": true, "legendas": true, "legendado": true, "legendada": true,
	"tradução": true, "traducao": true, "translation": true, "subtitles": true, "subs": true,
	"descrição": true, "descricao": true, "description": true,
	"cc": true, "hd": true, "hq": true, "4k": true, "visualizer": true, "visualiser": true,
}

// uploadFillers may join markers ("com letra na descrição") but are not tags
// on their own, so a song called "No" keeps its title.
var uploadFillers = map[string]bool{
	"com": true, "na": true, "no": true, "em": true, "e": true, "with": true, "in": true,
	"and": true, "the": true, "português": true, "portugues": true, "pt": true, "br": true,
}

// isUploadTag reports whether a title fragment only describes the upload.
func isUploadTag(s string) bool {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == ' ' || r == '/' || r == '+' || r == ',' || r == '.' || r == ':'
	})
	marker := false
	for _, w := range words {
		switch {
		case uploadMarkers[w]:
			marker = true
		case !uploadFillers[w]:
			return false
		}
	}
	return marker
}

// CleanTitle derives the track and artist to search for from a video's title
// and uploader. Titles conventionally read "Artist - Title (Official Video)";
// when there is no "Artist - " prefix the uploader is the best guess, minus the
// " - Topic" suffix YouTube gives auto-generated artist channels. Tags that
// describe the upload are dropped wherever they are, bracketed or as their own
// dash-separated segment, while other brackets such as a live venue are kept.
func CleanTitle(title, uploader string) (track, artist string) {
	title = bracketed.ReplaceAllStringFunc(title, func(group string) string {
		if isUploadTag(bracketed.FindStringSubmatch(group)[1]) {
			return " "
		}
		return group
	})

	var segments []string
	for _, seg := range splitSegments(title) {
		if !isUploadTag(seg) {
			segments = append(segments, seg)
		}
	}

	artist = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(uploader), " - Topic"))
	switch len(segments) {
	case 0:
		return strings.TrimSpace(title), artist
	case 1:
		return segments[0], artist
	}
	// Only the first separator splits: titles may contain dashes of their own.
	return strings.Join(segments[1:], " - "), segments[0]
}
