package audio

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/runnertest"
)

// WAV is what the module plays, so it must survive every request -- including
// one that asks only for compressed formats.
func TestResolveEncodingsAlwaysIncludesWAV(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{EncodingWAV}},
		{"lossy only", []string{EncodingOpus}, []string{EncodingWAV, EncodingOpus}},
		{"wav already asked for", []string{EncodingWAV}, []string{EncodingWAV}},
		{"duplicates collapse", []string{EncodingFLAC, EncodingFLAC}, []string{EncodingWAV, EncodingFLAC}},
		{"blank entries ignored", []string{"", " "}, []string{EncodingWAV}},
		{"case insensitive", []string{"OPUS"}, []string{EncodingWAV, EncodingOpus}},
		{
			"registry order, not argument order",
			[]string{EncodingMP3, EncodingOpus, EncodingFLAC},
			[]string{EncodingWAV, EncodingFLAC, EncodingOpus, EncodingMP3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveEncodings(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ids := make([]string, len(got))
			for i, e := range got {
				ids[i] = e.ID
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Errorf("ResolveEncodings(%v) = %v, want %v", tt.in, ids, tt.want)
			}
		})
	}
}

func TestResolveEncodingsRejectsUnknown(t *testing.T) {
	_, err := ResolveEncodings([]string{"wma"})
	if err == nil {
		t.Fatal("expected an unknown format to be rejected")
	}
	// The message should tell the user what they can pick instead.
	for _, id := range EncodingIDs() {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q should list the supported format %q", err, id)
		}
	}
}

func TestEncodingFilenameSwapsExtension(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{EncodingFLAC, "SongNoDrums.flac"},
		{EncodingOpus, "SongNoDrums.opus"},
		{EncodingAAC, "SongNoDrums.m4a"},
		{EncodingMP3, "SongNoDrums.mp3"},
		{EncodingWAV, "SongNoDrums.wav"},
	} {
		enc, ok := LookupEncoding(tc.id)
		if !ok {
			t.Fatalf("encoding %q missing from the registry", tc.id)
		}
		if got := enc.Filename("SongNoDrums.wav"); got != tc.want {
			t.Errorf("%s: Filename = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// Each format needs its own folder so one can be zipped and sent as-is.
func TestEncodingsHaveDistinctDirsAndExtensions(t *testing.T) {
	dirs := map[string]string{}
	exts := map[string]string{}
	for _, e := range Encodings() {
		if prev, dup := dirs[e.Dir]; dup {
			t.Errorf("encodings %q and %q share directory %q", prev, e.ID, e.Dir)
		}
		dirs[e.Dir] = e.ID
		if prev, dup := exts[e.Extension]; dup {
			t.Errorf("encodings %q and %q share extension %q", prev, e.ID, e.Extension)
		}
		exts[e.Extension] = e.ID
		if len(e.Args()) == 0 {
			t.Errorf("encoding %q has no encoder arguments", e.ID)
		}
	}
}

func TestLosslessFlags(t *testing.T) {
	for id, wantLossless := range map[string]bool{
		EncodingWAV: true, EncodingFLAC: true,
		EncodingOpus: false, EncodingAAC: false, EncodingMP3: false,
	} {
		enc, _ := LookupEncoding(id)
		if enc.Lossless != wantLossless {
			t.Errorf("%s: Lossless = %v, want %v", id, enc.Lossless, wantLossless)
		}
	}
}

// Args are returned by value; a caller must not be able to corrupt the registry.
func TestEncodingArgsAreCopied(t *testing.T) {
	enc, _ := LookupEncoding(EncodingFLAC)
	first := enc.Args()
	first[0] = "clobbered"
	if second := enc.Args(); second[0] == "clobbered" {
		t.Error("Args() exposes the registry's backing array")
	}
}

func TestEncodeInvokesTheRightCodec(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ id, codec string }{
		{EncodingFLAC, "flac"},
		{EncodingOpus, "libopus"},
		{EncodingAAC, "aac"},
		{EncodingMP3, "libmp3lame"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := runnertest.New()
			f.Handle("ffmpeg", nil, runnertest.Response{})
			enc, _ := LookupEncoding(tc.id)

			dst := filepath.Join(dir, tc.id, "out."+enc.Extension)
			if err := New(f).Encode(context.Background(), "in.wav", dst, enc, time.Minute, nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			c := f.CallsTo("ffmpeg")[0]
			if got, _ := c.ArgAfter("-c:a"); got != tc.codec {
				t.Errorf("codec = %q, want %q", got, tc.codec)
			}
			if !c.HasArg(dst) {
				t.Errorf("expected %s to be the output path", dst)
			}
		})
	}
}

// FLAC must never be given a bitrate: that would imply a lossy encode.
func TestLosslessEncodingsCarryNoBitrate(t *testing.T) {
	for _, e := range Encodings() {
		if !e.Lossless {
			continue
		}
		if strings.Contains(strings.Join(e.Args(), " "), "-b:a") {
			t.Errorf("lossless encoding %q specifies a bitrate", e.ID)
		}
	}
}
