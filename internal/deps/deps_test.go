package deps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
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

// fakePath puts executable stubs on PATH so exec.LookPath finds them, without
// the test depending on what the host has installed.
func fakePath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestMissingDenoIsReportedWithItsInstallHint(t *testing.T) {
	fakePath(t)

	st, ok := NewChecker(runnertest.New()).Check(context.Background()).Lookup("deno")
	if !ok {
		t.Fatal("deno should be part of the dependency report")
	}
	if st.OK() {
		t.Errorf("deno = %+v, want not found on an empty PATH", st)
	}
	if st.Hint != "brew install deno" {
		t.Errorf("hint = %q, want the brew install hint", st.Hint)
	}
}

func TestPresentDenoReportsItsVersion(t *testing.T) {
	fakePath(t, "deno")
	f := runnertest.New()
	f.Handle("deno", nil, runnertest.Response{Stdout: "deno 2.5.1 (stable, release, aarch64-apple-darwin)\nv8 14.0\ntypescript 5.9\n"})

	st, _ := NewChecker(f).Check(context.Background()).Lookup("deno")
	if !st.OK() || st.Version != "2.5.1" {
		t.Errorf("deno = %+v, want found with version 2.5.1", st)
	}
}

// fakeManagedPython creates the interpreter the managed venv would have, under
// an isolated data dir.
func fakeManagedPython(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	python := venvBin("python")
	if err := os.MkdirAll(filepath.Dir(python), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return python
}

func TestSwiftF0StatusReportsTheInstalledVersion(t *testing.T) {
	python := fakeManagedPython(t)
	f := runnertest.New()
	f.Handle("python", []string{"swift-f0"}, runnertest.Response{Stdout: "0.1.2\n"})

	st := NewChecker(f).swiftF0Status(context.Background())
	if !st.OK() || st.Version != "0.1.2" {
		t.Errorf("swift-f0 = %+v, want found with version 0.1.2", st)
	}
	if st.Path != python {
		t.Errorf("Path = %q, want the managed interpreter %q", st.Path, python)
	}
	if !st.Managed {
		t.Error("swift-f0 lives in the managed venv, so it must be marked Managed")
	}
}

func TestSwiftF0IsNotFoundWhenTheImportFails(t *testing.T) {
	fakeManagedPython(t)
	f := runnertest.New()
	f.Default = &runnertest.Response{Err: errors.New("PackageNotFoundError")}

	if st := NewChecker(f).swiftF0Status(context.Background()); st.OK() {
		t.Errorf("swift-f0 = %+v, want not found when the probe fails", st)
	}
}

func TestSwiftF0IsNotFoundWithoutAManagedEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	f := runnertest.New()
	if st := NewChecker(f).swiftF0Status(context.Background()); st.OK() {
		t.Errorf("swift-f0 = %+v, want not found with no venv", st)
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("%d commands ran, want none when there is no interpreter to ask", n)
	}
}

// The dtx report must not gain swift-f0; only the karaoke report asks for it,
// and it then treats both swift-f0 and deno as required.
func TestKaraokeReportRequiresWhatKaraokeNeeds(t *testing.T) {
	fakePath(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c := NewChecker(runnertest.New())

	if _, ok := c.Check(context.Background()).Lookup("swift-f0"); ok {
		t.Error("swift-f0 must not appear in the dtx report")
	}

	report := c.CheckKaraoke(context.Background())
	for _, name := range []string{"swift-f0", "deno", "ffmpeg", "yt-dlp", "demucs"} {
		st, ok := report.Lookup(name)
		if !ok || !st.Required {
			t.Errorf("%s = %+v (found=%v), want it required for karaoke", name, st, ok)
		}
	}
	if report.Ready() {
		t.Error("karaoke report should not be ready on an empty machine")
	}
}

// installFake answers an install the way a healthy machine would: uv builds the
// venv, "installing" demucs drops its executable and an interpreter into it,
// and the verification runs succeed. failOn names a package whose install
// should fail.
func installFake(failOn string) *runnertest.Fake {
	f := runnertest.New()
	f.HandleFunc("", nil, installResponder(failOn))
	return f
}

func installResponder(failOn string) func(runner.Spec) runnertest.Response {
	return func(spec runner.Spec) runnertest.Response {
		if filepath.Base(spec.Name) == "uv" && len(spec.Args) > 0 && spec.Args[0] == "pip" {
			pkg := spec.Args[len(spec.Args)-1]
			if pkg == failOn {
				return runnertest.Response{Err: errors.New("no matching distribution for " + pkg)}
			}
			return runnertest.Response{Do: func(runner.Spec) error {
				if pkg != "demucs" {
					return nil
				}
				for _, name := range []string{"demucs", "python"} {
					p := venvBin(name)
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
						return err
					}
				}
				return nil
			}}
		}
		return runnertest.Response{}
	}
}

func installedPackages(f *runnertest.Fake) []string {
	var pkgs []string
	for _, c := range f.Calls() {
		if filepath.Base(c.Name) == "uv" && len(c.Args) > 0 && c.Args[0] == "pip" {
			pkgs = append(pkgs, c.Args[len(c.Args)-1])
		}
	}
	return pkgs
}

func TestInstallAddsSwiftF0AfterDemucsAndVerifiesBoth(t *testing.T) {
	fakePath(t, "uv")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := installFake("")

	path, err := NewChecker(f).InstallDemucs(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != ManagedDemucsPath() {
		t.Errorf("path = %q, want the managed demucs executable", path)
	}

	if got := strings.Join(installedPackages(f), ","); got != "demucs,numpy,swift-f0" {
		t.Errorf("install order = %s, want demucs,numpy,swift-f0", got)
	}
	var demucsRuns, importChecks int
	for _, c := range f.Calls() {
		switch {
		case c.Name == path && c.HasArg("--help"):
			demucsRuns++
		case filepath.Base(c.Name) == "python" && c.HasArg("import swift_f0"):
			importChecks++
		}
	}
	if demucsRuns != 1 || importChecks != 1 {
		t.Errorf("demucs runs = %d, swift_f0 import checks = %d, want one of each", demucsRuns, importChecks)
	}
}

func TestSwiftF0InstallFailureKeepsDemucsAndSaysWhichPackageFailed(t *testing.T) {
	fakePath(t, "uv")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := installFake("swift-f0")

	path, err := NewChecker(f).InstallDemucs(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "swift-f0") {
		t.Fatalf("err = %v, want an error naming swift-f0", err)
	}
	if !errors.Is(err, ErrSwiftF0) {
		t.Errorf("err = %v, want it to match ErrSwiftF0 so callers can tell it apart", err)
	}
	if path == "" || path != ManagedDemucsPath() {
		t.Errorf("path = %q, want the working demucs path returned alongside the error", path)
	}
	if _, statErr := os.Stat(VenvDir()); statErr != nil {
		t.Errorf("the venv should be left in place: %v", statErr)
	}
	for _, c := range f.Calls() {
		if c.HasArg("uninstall") {
			t.Errorf("unexpected removal call: %s", c)
		}
	}
}

func TestSwiftF0ImportFailureAfterInstallIsReported(t *testing.T) {
	fakePath(t, "uv")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := runnertest.New()
	f.HandleFunc("python", []string{"import swift_f0"}, func(runner.Spec) runnertest.Response {
		return runnertest.Response{Err: errors.New("ModuleNotFoundError")}
	})
	f.HandleFunc("", nil, installResponder(""))

	path, err := NewChecker(f).InstallDemucs(context.Background(), nil)
	if !errors.Is(err, ErrSwiftF0) || !strings.Contains(err.Error(), "swift_f0") {
		t.Errorf("err = %v, want an ErrSwiftF0 mentioning the failed import", err)
	}
	if path == "" {
		t.Error("the working demucs path should still be returned")
	}
}

// yt-dlp only needs deno for YouTube, and dtx also works from local files, so
// a machine without deno must not lose dtx.
func TestMissingDenoDoesNotBlockDtx(t *testing.T) {
	fakePath(t)

	report := NewChecker(runnertest.New()).Check(context.Background())
	if st, _ := report.Lookup("deno"); st.Required {
		t.Error("deno must be optional in the dtx report")
	}
}

func TestVenvPythonLivesInsideTheManagedEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	got := VenvPython()
	if !strings.HasPrefix(got, VenvDir()) || !strings.Contains(filepath.Base(got), "python") {
		t.Errorf("VenvPython() = %q, want a python inside %q", got, VenvDir())
	}
}

func TestSwiftF0AloneIsAddedToTheExistingEnvironmentWithoutRebuildingIt(t *testing.T) {
	fakePath(t, "uv")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	python := VenvPython()
	if err := os.MkdirAll(filepath.Dir(python), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := installFake("")

	if err := NewChecker(f).InstallSwiftF0(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range f.Calls() {
		if filepath.Base(c.Name) == "uv" && len(c.Args) > 0 && c.Args[0] == "venv" {
			t.Errorf("rebuilt the environment: %v", c.Args)
		}
	}
	if got := strings.Join(installedPackages(f), ","); got != "swift-f0" {
		t.Errorf("installed %s, want only swift-f0", got)
	}
	var imports int
	for _, c := range f.Calls() {
		if c.Name == python && c.HasArg("import swift_f0") {
			imports++
		}
	}
	if imports != 1 {
		t.Errorf("swift_f0 import checks = %d, want 1", imports)
	}
}

func TestSwiftF0AloneNeedsAnEnvironmentToAddItTo(t *testing.T) {
	fakePath(t, "uv")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := installFake("")

	err := NewChecker(f).InstallSwiftF0(context.Background(), nil)
	if !errors.Is(err, ErrNoManagedEnvironment) {
		t.Fatalf("err = %v, want ErrNoManagedEnvironment", err)
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("ran %d commands, want none", n)
	}
}

func TestTheSwiftF0HintPointsAtTheKaraokeDoctor(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, _ := NewChecker(runnertest.New()).CheckKaraoke(context.Background()).Lookup(SwiftF0Package)
	if !strings.Contains(st.Hint, "cli karaoke doctor --install") {
		t.Errorf("hint = %q, want it to name cli karaoke doctor --install", st.Hint)
	}
}
