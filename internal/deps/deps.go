// Package deps checks for the external tools the pipeline needs and, for
// Demucs, can provision them.
//
// Demucs is a Python program with a heavyweight dependency tree (PyTorch), so
// rather than requiring the user to manage a Python environment, this package
// builds an isolated one with uv and keeps it out of the way of any Python the
// user has set up for their own work.
package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/youtube"
)

// pythonVersion is the interpreter provisioned for Demucs. It deliberately
// trails the newest release: PyTorch wheels lag Python releases by months, and
// a version with no wheels would force a source build.
const pythonVersion = "3.12"

// uvTool is the executable used to build the managed environment.
const uvTool = "uv"

// Status describes whether one external tool is usable.
type Status struct {
	Name string
	// Path is the resolved executable location, empty when not found.
	Path string
	// Version is a short version string, empty when it could not be determined.
	Version string
	// Required marks tools the pipeline cannot run without.
	Required bool
	// Managed marks tools this program can install on the user's behalf.
	Managed bool
	// Hint explains how to install the tool when it is missing.
	Hint string
}

// OK reports whether the tool was found.
func (s Status) OK() bool { return s.Path != "" }

// Report is the outcome of checking every dependency.
type Report struct {
	Tools []Status
}

// Missing returns the required tools that are not installed.
func (r Report) Missing() []Status {
	var out []Status
	for _, t := range r.Tools {
		if t.Required && !t.OK() {
			out = append(out, t)
		}
	}
	return out
}

// Ready reports whether every required tool is present.
func (r Report) Ready() bool { return len(r.Missing()) == 0 }

// Lookup returns the status of a named tool.
func (r Report) Lookup(name string) (Status, bool) {
	for _, t := range r.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Status{}, false
}

// Checker inspects the environment.
type Checker struct {
	Run runner.Runner
}

// NewChecker returns a Checker backed by r.
func NewChecker(r runner.Runner) *Checker { return &Checker{Run: r} }

// Check inspects every dependency and reports what it found.
func (c *Checker) Check(ctx context.Context) Report {
	return Report{Tools: []Status{
		c.probe(ctx, audio.FFmpeg, true, false, "brew install ffmpeg", "-version"),
		c.probe(ctx, audio.FFprobe, true, false, "brew install ffmpeg", "-version"),
		c.probe(ctx, youtube.Tool, true, false, "brew install yt-dlp", "--version"),
		c.demucsStatus(ctx),
		c.probe(ctx, uvTool, false, false, "brew install uv", "--version"),
	}}
}

// probe locates a tool and asks it for its version.
func (c *Checker) probe(ctx context.Context, name string, required, managed bool, hint string, versionArgs ...string) Status {
	s := Status{Name: name, Required: required, Managed: managed, Hint: hint}
	path, err := exec.LookPath(name)
	if err != nil {
		return s
	}
	s.Path = path
	s.Version = c.version(ctx, path, versionArgs...)
	return s
}

// version runs a tool's version flag and extracts something short and useful.
func (c *Checker) version(ctx context.Context, path string, args ...string) string {
	if len(args) == 0 {
		return ""
	}
	res, err := c.Run.Run(ctx, runner.Spec{Name: path, Args: args})
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(firstLine(res.Stdout))
	if line == "" {
		return ""
	}
	// Version banners vary wildly -- "ffmpeg version 8.1.1 Copyright...",
	// "uv 0.11.17 (Homebrew ... aarch64-apple-darwin)", bare "2026.03.17" --
	// so take the first field that actually looks like a version number
	// rather than guessing at a fixed position.
	for _, f := range strings.Fields(line) {
		if v := strings.TrimPrefix(f, "v"); versionish.MatchString(v) {
			return v
		}
	}
	return line
}

// versionish matches a dotted numeric version, optionally with a suffix such
// as "-rc1".
var versionish = regexp.MustCompile(`^\d+(\.\d+)+([-.\w]*)$`)

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// demucsStatus finds Demucs, preferring a copy already on PATH over the managed
// environment so a user who installed it themselves keeps control of it.
func (c *Checker) demucsStatus(ctx context.Context) Status {
	s := Status{
		Name:     separate.Tool,
		Required: true,
		Managed:  true,
		Hint:     "run 'cli dtx doctor --install' to set it up automatically",
	}
	if path, err := exec.LookPath(separate.Tool); err == nil {
		s.Path = path
	} else if managed := ManagedDemucsPath(); managed != "" {
		s.Path = managed
	}
	if s.Path != "" {
		s.Version = c.demucsVersion(ctx, s.Path)
	}
	return s
}

// demucsVersion asks the interpreter beside the demucs script for the installed
// package version. Demucs itself has no reliable --version flag.
func (c *Checker) demucsVersion(ctx context.Context, demucsPath string) string {
	python := filepath.Join(filepath.Dir(demucsPath), "python")
	if _, err := os.Stat(python); err != nil {
		return ""
	}
	res, err := c.Run.Run(ctx, runner.Spec{
		Name: python,
		Args: []string{"-c", "import demucs; print(demucs.__version__)"},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(firstLine(res.Stdout))
}

// dataDir is where the managed Demucs environment lives, following the XDG
// convention so it sits alongside other user-level application data.
func dataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "dtx")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "dtx")
	}
	return filepath.Join(home, ".local", "share", "dtx")
}

// VenvDir is the location of the managed Python environment.
func VenvDir() string { return filepath.Join(dataDir(), "demucs-venv") }

// ManagedDemucsPath returns the path to the managed Demucs executable, or an
// empty string when it has not been installed.
func ManagedDemucsPath() string {
	path := venvBin("demucs")
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

// venvBin resolves an executable inside the managed environment, accounting for
// the different layout used on Windows.
func venvBin(name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(VenvDir(), "Scripts", name+".exe")
	}
	return filepath.Join(VenvDir(), "bin", name)
}

// InstallDemucs builds the managed environment and installs Demucs into it,
// returning the path to the resulting executable.
//
// onLine, when non-nil, receives installer output so a caller can show progress
// during what is a multi-minute, multi-gigabyte download.
func (c *Checker) InstallDemucs(ctx context.Context, onLine runner.LineFunc) (string, error) {
	if _, err := exec.LookPath(uvTool); err != nil {
		return "", fmt.Errorf("uv is required to install demucs automatically; install it with 'brew install uv', or install demucs yourself and make sure it is on PATH")
	}

	venv := VenvDir()
	if err := os.MkdirAll(filepath.Dir(venv), 0o755); err != nil {
		return "", fmt.Errorf("creating data directory: %w", err)
	}

	if _, err := c.Run.Run(ctx, runner.Spec{
		Name:   uvTool,
		Args:   []string{"venv", "--python", pythonVersion, venv},
		OnLine: onLine,
	}); err != nil {
		return "", fmt.Errorf("creating python environment: %w", err)
	}

	python := venvBin("python")
	install := func(target string) error {
		_, err := c.Run.Run(ctx, runner.Spec{
			Name:   uvTool,
			Args:   []string{"pip", "install", "--python", python, target},
			OnLine: onLine,
		})
		return err
	}

	err := install("demucs")
	if err != nil && ctx.Err() == nil {
		// The published release occasionally lags its own dependencies. The
		// repository's main branch carries the compatibility fixes, so it is
		// worth one retry before giving up.
		if gitErr := install("git+https://github.com/adefossez/demucs#egg=demucs"); gitErr == nil {
			err = nil
		} else {
			err = fmt.Errorf("%w (installing from source also failed: %v)", err, gitErr)
		}
	}
	if err != nil {
		return "", fmt.Errorf("installing demucs: %w", err)
	}

	for _, pkg := range undeclaredDeps {
		if err := install(pkg); err != nil {
			return "", fmt.Errorf("installing %s (required by demucs): %w", pkg, err)
		}
	}

	path := ManagedDemucsPath()
	if path == "" {
		return "", fmt.Errorf("installing demucs: install reported success but no executable appeared in %s", venv)
	}
	// Importing demucs pulls in torch and numpy, so a trivial run is enough to
	// prove the environment actually works. Without this a broken install is
	// only discovered minutes into a pipeline run, after a download has
	// already happened.
	if err := c.verify(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

// undeclaredDeps are packages demucs imports at runtime but omits from its
// package metadata. demucs 4.1.0 imports numpy in transformer.py yet does not
// list it, and torch no longer installs numpy transitively -- so a default
// install succeeds and then dies with ModuleNotFoundError on first use.
var undeclaredDeps = []string{"numpy"}

// verify runs demucs once to confirm the environment imports cleanly.
func (c *Checker) verify(ctx context.Context, path string) error {
	if _, err := c.Run.Run(ctx, runner.Spec{Name: path, Args: []string{"--help"}}); err != nil {
		return fmt.Errorf("demucs installed but does not run: %w", err)
	}
	return nil
}

// RemoveManagedDemucs deletes the managed environment.
func RemoveManagedDemucs() error {
	venv := VenvDir()
	if _, err := os.Stat(venv); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(venv)
}
