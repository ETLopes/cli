package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdout(t *testing.T) {
	res, err := New().Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", "echo hello; echo world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "hello\nworld" {
		t.Errorf("Stdout = %q, want %q", got, "hello\nworld")
	}
}

func TestRunStreamsLinesAsProduced(t *testing.T) {
	var out, errs []string
	_, err := New().Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", "echo to-stdout; echo to-stderr >&2"},
		OnLine: func(s Stream, line string) {
			if s == Stdout {
				out = append(out, line)
			} else {
				errs = append(errs, line)
			}
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != "to-stdout" {
		t.Errorf("stdout lines = %v, want [to-stdout]", out)
	}
	if len(errs) != 1 || errs[0] != "to-stderr" {
		t.Errorf("stderr lines = %v, want [to-stderr]", errs)
	}
}

// Progress bars redraw with carriage returns; splitting on newlines alone
// would withhold every update until the process exited.
func TestRunSplitsCarriageReturnProgress(t *testing.T) {
	var lines []string
	_, err := New().Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", `printf '10%%\r50%%\r100%%\n'`},
		OnLine: func(_ Stream, l string) {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"10%", "50%", "100%"}
	if len(lines) != len(want) {
		t.Fatalf("got %d progress lines %v, want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestRunReportsExitCodeAndOutput(t *testing.T) {
	_, err := New().Run(context.Background(), Spec{
		Name: "sh", Args: []string{"-c", "echo 'the reason it failed' >&2; exit 3"},
	})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error was %T, want *ExitError", err)
	}
	if ee.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", ee.ExitCode)
	}
	// The tool's own message is what explains the failure.
	if !strings.Contains(err.Error(), "the reason it failed") {
		t.Errorf("error should include the tool output, got: %v", err)
	}
}

func TestRunReportsMissingExecutable(t *testing.T) {
	_, err := New().Run(context.Background(), Spec{Name: "definitely-not-a-real-binary-xyz"})
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error was %T (%v), want *NotFoundError", err, err)
	}
}

// A cancelled run is the caller's doing and must be reported as such, not as
// an opaque tool failure.
func TestRunCancellationReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := New().Run(ctx, Spec{Name: "sh", Args: []string{"-c", "sleep 5"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

// Only the tail of a chatty tool's output is worth keeping.
func TestRunBoundsCapturedOutput(t *testing.T) {
	_, err := New().Run(context.Background(), Spec{
		Name: "sh",
		Args: []string{"-c", "for i in $(seq 1 500); do echo line$i >&2; done; exit 1"},
	})
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error was %T, want *ExitError", err)
	}
	if len(ee.Output) > maxCapturedLines {
		t.Errorf("captured %d lines, want at most %d", len(ee.Output), maxCapturedLines)
	}
	// The retained lines must be the most recent ones.
	if !strings.Contains(strings.Join(ee.Output, "\n"), "line500") {
		t.Error("expected the tail of the output to be retained")
	}
}

func TestRingKeepsInsertionOrder(t *testing.T) {
	r := newRing(3)
	for _, s := range []string{"a", "b", "c", "d"} {
		r.add(s)
	}
	got := strings.Join(r.slice(), ",")
	if got != "b,c,d" {
		t.Errorf("ring contents = %q, want %q", got, "b,c,d")
	}
}
