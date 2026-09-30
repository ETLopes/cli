package reference

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/runnertest"
)

const validContourJSON = `{"hop":0.016,"sample_rate":16000,"time":[0,0.016],"hz":[220,221],"confidence":[0.9,0.9],"loudness_db":[-20,-20]}`

func TestSwiftF0RunsTheEmbeddedScriptWithTheVenvPython(t *testing.T) {
	dir := t.TempDir()
	wav, out := filepath.Join(dir, "vocals16k.wav"), filepath.Join(dir, "reference.json")

	f := runnertest.New()
	f.HandleFunc("/venv/bin/python", nil, func(spec runner.Spec) runnertest.Response {
		return runnertest.Response{Do: func(spec runner.Spec) error {
			// The script writes to its second positional argument.
			return os.WriteFile(spec.Args[len(spec.Args)-1], []byte(validContourJSON), 0o644)
		}}
	})

	if err := (&SwiftF0{Run: f, Python: "/venv/bin/python"}).Extract(context.Background(), wav, out); err != nil {
		t.Fatal(err)
	}

	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "/venv/bin/python" {
		t.Fatalf("calls = %v", calls)
	}
	args := calls[0].Args
	if len(args) != 4 || args[0] != "-c" || args[1] != Script() || args[2] != wav {
		t.Errorf("args = %q; want -c <script> <wav> <tmp out>", args)
	}
	if args[3] == out {
		t.Error("the script must write a temp file so a failed run never leaves a truncated reference.json")
	}

	c, err := Load(out)
	if err != nil {
		t.Fatalf("the output was not moved into place: %v", err)
	}
	if c.Len() != 2 {
		t.Errorf("frames = %d", c.Len())
	}
	if _, err := os.Stat(args[3]); !os.IsNotExist(err) {
		t.Error("the temp file should have been renamed away")
	}
}

func TestSwiftF0FailureCarriesTheScriptOutputTail(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "reference.json")
	f := runnertest.New()
	f.Handle("python", nil, runnertest.Response{Err: &runner.ExitError{
		Command: "python -c ...", ExitCode: 1,
		Output: []string{"Traceback (most recent call last):", "ModuleNotFoundError: No module named 'swift_f0'"},
	}})

	err := (&SwiftF0{Run: f, Python: "python"}).Extract(context.Background(), "in.wav", out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "No module named 'swift_f0'") {
		t.Errorf("error lost the script's own explanation: %v", err)
	}
	var ee *runner.ExitError
	if !errors.As(err, &ee) {
		t.Error("the runner error should stay in the chain")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("a failed run must not leave an output file")
	}
}

func TestSwiftF0RejectsScriptOutputThatIsNotAContour(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "reference.json")
	f := runnertest.New()
	f.Handle("python", nil, runnertest.Response{Do: func(spec runner.Spec) error {
		return os.WriteFile(spec.Args[len(spec.Args)-1], []byte(`{"hop":0.016,"time":[0,0.016],"hz":[1]}`), 0o644)
	}})
	if err := (&SwiftF0{Run: f, Python: "python"}).Extract(context.Background(), "in.wav", out); err == nil {
		t.Fatal("expected a validation error")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("an invalid contour must not be installed")
	}
}

func TestEmbeddedScriptIsPresentAndReadsTheArgvGoPasses(t *testing.T) {
	// No Python here: this pins the script's command-line contract from the
	// outside. Go passes "-c <script> <wav> <out>", so inside the script the
	// WAV is argv[1] and the output path argv[2].
	s := Script()
	if strings.TrimSpace(s) == "" {
		t.Fatal("the script was not embedded")
	}
	for _, want := range []string{"sys.argv[1]", "sys.argv[2]", "from swift_f0 import SwiftF0", "import wave"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(s, "sys.argv[3]") {
		t.Error("script reads more arguments than Go passes")
	}
	if strings.Contains(s, "detect_file") {
		t.Error("detect_file may need the optional soundfile package")
	}
	// The JSON keys must be exactly the ones the Contour type reads.
	for _, key := range []string{"hop", "sample_rate", "time", "hz", "confidence", "loudness_db"} {
		if !regexp.MustCompile(`"` + key + `"\s*:`).MatchString(s) {
			t.Errorf("script does not write the %q key", key)
		}
	}
}
