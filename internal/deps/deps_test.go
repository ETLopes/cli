package deps

import (
	"context"
	"strings"
	"testing"

	"github.com/eduardolopes/dtx/internal/runnertest"
)

// Version banners differ wildly between tools, so the parser has to find the
// version rather than assume a fixed field position.
func TestVersionParsing(t *testing.T) {
	tests := []struct {
		name   string
		banner string
		want   string
	}{
		{"ffmpeg", "ffmpeg version 8.1.1 Copyright (c) 2000-2026 the FFmpeg developers", "8.1.1"},
		{"yt-dlp", "2026.03.17", "2026.03.17"},
		{"uv", "uv 0.11.17 (Homebrew 2026-05-28 aarch64-apple-darwin)", "0.11.17"},
		{"demucs", "4.0.1", "4.0.1"},
		{"v-prefixed", "tool v1.2.3", "1.2.3"},
		{"multiline takes the first line", "ffmpeg version 7.0 blah\nbuilt with clang", "7.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := runnertest.New()
			f.Handle("tool", nil, runnertest.Response{Stdout: tt.banner})

			got := NewChecker(f).version(context.Background(), "tool", "--version")
			if got != tt.want {
				t.Errorf("version(%q) = %q, want %q", tt.banner, got, tt.want)
			}
		})
	}
}

func TestVersionOfUnavailableToolIsEmpty(t *testing.T) {
	f := runnertest.New()
	f.Default = &runnertest.Response{Err: context.DeadlineExceeded}

	if got := NewChecker(f).version(context.Background(), "tool", "--version"); got != "" {
		t.Errorf("version = %q, want empty when the tool cannot be run", got)
	}
}

func TestReportTracksRequiredTools(t *testing.T) {
	r := Report{Tools: []Status{
		{Name: "ffmpeg", Path: "/usr/bin/ffmpeg", Required: true},
		{Name: "demucs", Required: true, Managed: true, Hint: "run dtx doctor --install"},
		{Name: "uv", Required: false},
	}}

	if r.Ready() {
		t.Error("Ready should be false while a required tool is missing")
	}
	missing := r.Missing()
	if len(missing) != 1 || missing[0].Name != "demucs" {
		t.Errorf("Missing() = %+v, want just demucs", missing)
	}
	// An optional tool being absent must not block a run.
	if got, ok := r.Lookup("uv"); !ok || got.OK() {
		t.Errorf("Lookup(uv) = %+v, want a found-but-not-installed entry", got)
	}

	r.Tools[1].Path = "/somewhere/demucs"
	if !r.Ready() {
		t.Error("Ready should be true once every required tool is present")
	}
}

func TestVenvPathsAreScopedToTheDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-test")

	venv := VenvDir()
	if !strings.HasPrefix(venv, "/tmp/xdg-test/dtx") {
		t.Errorf("VenvDir() = %q, want it under the XDG data dir", venv)
	}
	// The managed environment must never be reported as present when absent.
	if got := ManagedDemucsPath(); got != "" {
		t.Errorf("ManagedDemucsPath() = %q, want empty when nothing is installed", got)
	}
}

func TestRemoveManagedDemucsIsIdempotent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := RemoveManagedDemucs(); err != nil {
		t.Errorf("removing a non-existent environment should succeed, got: %v", err)
	}
}
