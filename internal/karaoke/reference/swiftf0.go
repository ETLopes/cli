package reference

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/ETLopes/cli/internal/deps"
	"github.com/ETLopes/cli/internal/runner"
)

//go:embed swiftf0.py
var script string

// Script is the embedded SwiftF0 extraction script.
func Script() string { return script }

// SwiftF0 extracts the contour with the SwiftF0 pitch tracker, which lives in
// the managed Python environment beside Demucs.
//
// The script is shipped inside the binary and passed to the interpreter with
// -c, so there is no file to keep in sync with the installed version and no
// stdin for the runner to support. It runs as a batch step and exits.
type SwiftF0 struct {
	Run runner.Runner
	// Python is the interpreter to use. Empty resolves the managed venv's.
	Python string
}

func (s *SwiftF0) python() string {
	if s.Python != "" {
		return s.Python
	}
	return deps.VenvPython()
}

// Extract implements Extractor. The script writes to a temporary sibling of out
// that is validated and renamed into place, so an interrupted or broken run
// never leaves a plausible-looking but truncated reference.json for the
// resume logic to trust.
func (s *SwiftF0) Extract(ctx context.Context, wav, out string) error {
	tmp := out + ".tmp"
	defer os.Remove(tmp)

	if _, err := s.Run.Run(ctx, runner.Spec{
		Name: s.python(),
		Args: []string{"-c", script, wav, tmp},
	}); err != nil {
		return fmt.Errorf("extracting pitch contour: %w", err)
	}
	if _, err := Load(tmp); err != nil {
		return fmt.Errorf("extracting pitch contour: %w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return fmt.Errorf("extracting pitch contour: %w", err)
	}
	return nil
}
