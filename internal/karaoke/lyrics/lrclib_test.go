package lyrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLRCLIB is a scriptable stand-in for the LRCLIB API that records the
// requests it receives.
type fakeLRCLIB struct {
	mu       sync.Mutex
	requests []*http.Request
	get      func(w http.ResponseWriter, r *http.Request)
	search   func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeLRCLIB) start(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/get":
			if f.get != nil {
				f.get(w, r)
				return
			}
		case "/api/search":
			if f.search != nil {
				f.search(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL + "/api", Version: "test"}
}

func (f *fakeLRCLIB) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.URL.Path)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestFindReturnsTheExactMatchFromGet(t *testing.T) {
	f := &fakeLRCLIB{get: func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("track_name") != "Placeholder Title" || q.Get("artist_name") != "Placeholder Artist" || q.Get("duration") != "200" {
			t.Errorf("unexpected query %v", q)
		}
		writeJSON(w, map[string]any{"id": 7, "trackName": "Placeholder Title", "artistName": "Placeholder Artist",
			"duration": 200, "instrumental": false, "syncedLyrics": "[00:01.00]la la la"})
	}}
	c := f.start(t)

	got, ok, err := c.Find(context.Background(), "Placeholder Title", "Placeholder Artist", 200*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.ID != 7 || got.SyncedLyrics != "[00:01.00]la la la" {
		t.Errorf("got %+v", got)
	}
	if paths := f.paths(); len(paths) != 1 {
		t.Errorf("a /get hit must not fall through to /search, requests: %v", paths)
	}
}

func TestFindSendsAnIdentifyingUserAgent(t *testing.T) {
	var ua string
	f := &fakeLRCLIB{get: func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		writeJSON(w, map[string]any{"id": 1})
	}}
	c := f.start(t)
	c.Version = "v1.2.3"
	if _, _, err := c.Find(context.Background(), "t", "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	want := "cli-karaoke/v1.2.3 (https://github.com/ETLopes/cli)"
	if ua != want {
		t.Errorf("User-Agent = %q, want %q", ua, want)
	}
}

func TestFindSearchesWhenGetIs404AndPicksTheCandidateWithinTwoSeconds(t *testing.T) {
	f := &fakeLRCLIB{
		search: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []map[string]any{
				{"id": 1, "duration": 210, "syncedLyrics": "[00:01.00]far away"},
				{"id": 2, "duration": 201, "syncedLyrics": "[00:01.00]la la la"},
			})
		},
	}
	c := f.start(t)

	got, ok, err := c.Find(context.Background(), "Placeholder Title", "Placeholder Artist", 200*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.ID != 2 {
		t.Errorf("picked id %d, want 2 (1 s off, not 10 s)", got.ID)
	}
	if paths := f.paths(); len(paths) != 2 || paths[0] != "/api/get" || paths[1] != "/api/search" {
		t.Errorf("requests = %v", paths)
	}
}

func TestFindRejectsSearchCandidatesOutsideTheDurationWindow(t *testing.T) {
	f := &fakeLRCLIB{search: func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "duration": 210, "syncedLyrics": "[00:01.00]x"}})
	}}
	c := f.start(t)
	_, ok, err := c.Find(context.Background(), "t", "a", 200*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a 10 s mismatch is a different recording, not a match")
	}
}

func TestFindPrefersSyncedLyricsAmongCandidatesInTheWindow(t *testing.T) {
	f := &fakeLRCLIB{search: func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "duration": 200, "plainLyrics": "plain words"},
			{"id": 2, "duration": 201.5, "syncedLyrics": "[00:01.00]la la la"},
		})
	}}
	got, ok, err := f.start(t).Find(context.Background(), "t", "a", 200*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.ID != 2 {
		t.Errorf("picked id %d, want the synced candidate 2", got.ID)
	}
}

func TestFindFallsBackToAFreeTextQueryWhenFieldSearchIsEmpty(t *testing.T) {
	f := &fakeLRCLIB{search: func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") == "" {
			writeJSON(w, []map[string]any{})
			return
		}
		if !strings.Contains(q.Get("q"), "Placeholder Title") {
			t.Errorf("q = %q", q.Get("q"))
		}
		writeJSON(w, []map[string]any{{"id": 9, "duration": 200, "syncedLyrics": "[00:01.00]x"}})
	}}
	got, ok, err := f.start(t).Find(context.Background(), "Placeholder Title", "Placeholder Artist", 200*time.Second)
	if err != nil || !ok || got.ID != 9 {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestFindReportsNoMatchWhenNothingIsFound(t *testing.T) {
	f := &fakeLRCLIB{search: func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) }}
	_, ok, err := f.start(t).Find(context.Background(), "t", "a", time.Minute)
	if err != nil || ok {
		t.Errorf("ok=%v err=%v, want a clean no-match", ok, err)
	}
}

func TestFindFailsOnServerErrors(t *testing.T) {
	f := &fakeLRCLIB{get: func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}}
	_, _, err := f.start(t).Find(context.Background(), "t", "a", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want one mentioning the 500", err)
	}
}

func TestFindFailsOnNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing is listening any more
	c := &Client{BaseURL: url + "/api"}
	if _, _, err := c.Find(context.Background(), "t", "a", time.Minute); err == nil {
		t.Error("expected a network error")
	}
}

func TestFindHonoursContextCancellation(t *testing.T) {
	f := &fakeLRCLIB{}
	c := f.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Find(ctx, "t", "a", time.Minute); err == nil {
		t.Error("expected an error from a cancelled context")
	}
}

func TestTrackHasLyricsIsFalseForInstrumentalsAndEmptyEntries(t *testing.T) {
	for name, tr := range map[string]Track{
		"instrumental": {Instrumental: true, SyncedLyrics: "[00:01.00]x"},
		"empty":        {},
		"blank":        {SyncedLyrics: "  ", PlainLyrics: "\n"},
	} {
		if tr.HasLyrics() {
			t.Errorf("%s: HasLyrics = true", name)
		}
	}
	if !(Track{PlainLyrics: "words"}).HasLyrics() || !(Track{SyncedLyrics: "[00:01.00]x"}).HasLyrics() {
		t.Error("plain or synced text should count as lyrics")
	}
}

func TestCleanTitleDerivesTrackAndArtist(t *testing.T) {
	tests := []struct {
		name, title, uploader, track, artist string
	}{
		{"artist dash title with official video", "Placeholder Artist - Placeholder Title (Official Video)", "Some Channel", "Placeholder Title", "Placeholder Artist"},
		{"bracketed lyrics suffix uses uploader", "Placeholder Title [Lyrics]", "Placeholder Artist", "Placeholder Title", "Placeholder Artist"},
		{"topic channel loses its suffix", "Placeholder Title", "Placeholder Artist - Topic", "Placeholder Title", "Placeholder Artist"},
		{"official music video brackets", "Placeholder Artist - Placeholder Title [Official Music Video]", "x", "Placeholder Title", "Placeholder Artist"},
		{"audio suffix", "Placeholder Artist - Placeholder Title (Audio)", "x", "Placeholder Title", "Placeholder Artist"},
		{"case insensitive suffix", "Placeholder Title (OFFICIAL VIDEO)", "Placeholder Artist", "Placeholder Title", "Placeholder Artist"},
		{"dashes inside the title survive", "Placeholder Artist - Placeholder - Part Two", "x", "Placeholder - Part Two", "Placeholder Artist"},
		{"plain title keeps uploader", "Placeholder Title", "Placeholder Artist", "Placeholder Title", "Placeholder Artist"},
		{"an unrelated parenthesis is kept", "Placeholder Title (Live in Placeholder City)", "Placeholder Artist", "Placeholder Title (Live in Placeholder City)", "Placeholder Artist"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			track, artist := CleanTitle(tc.title, tc.uploader)
			if track != tc.track || artist != tc.artist {
				t.Errorf("got (%q, %q), want (%q, %q)", track, artist, tc.track, tc.artist)
			}
		})
	}
}
