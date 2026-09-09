// Package runner executes the external tools the pipeline orchestrates
// (yt-dlp, ffmpeg, ffprobe, demucs) with a single, consistent contract:
// context cancellation works, output can be observed line by line as it is
// produced, and a failure carries enough of the tool's own output to be
// diagnosable without re-running anything.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// maxCapturedLines bounds how much tool output is retained for error reporting.
// Tools like demucs emit thousands of progress lines; only the tail explains a
// failure, and keeping all of it would balloon memory on long runs.
const maxCapturedLines = 40

// Stream identifies which pipe a line of output arrived on.
type Stream int

const (
	Stdout Stream = iota
	Stderr
)

// LineFunc observes a single line of tool output as it is produced. It must not
// block: it is called from the goroutine draining the pipe, so a slow consumer
// slows the child process. It is called from multiple goroutines (one per
// stream) and so must be safe for concurrent use.
type LineFunc func(Stream, string)

// Spec describes one external command invocation.
type Spec struct {
	Name string   // executable path or name resolvable on PATH
	Args []string // arguments, not including Name
	Dir  string   // working directory; empty means the current one
	Env  []string // extra KEY=VALUE entries appended to the parent environment

	// OnLine, when non-nil, receives each line of stdout and stderr as it is
	// produced. Used to drive progress reporting.
	OnLine LineFunc
}

// String renders the command roughly as it would be typed, for logs and errors.
func (s Spec) String() string {
	return strings.TrimSpace(s.Name + " " + strings.Join(s.Args, " "))
}

// Result carries the outcome of a completed command.
type Result struct {
	Stdout   string // full stdout, useful for tools queried for JSON
	ExitCode int
}

// ExitError reports a command that ran but exited non-zero. It embeds the tail
// of the tool's own output, which is almost always the actual explanation.
type ExitError struct {
	Command  string
	ExitCode int
	Output   []string
}

func (e *ExitError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: exited with code %d", firstWord(e.Command), e.ExitCode)
	if len(e.Output) > 0 {
		b.WriteString("\n\n")
		for _, line := range e.Output {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// NotFoundError reports that an executable is not installed or not on PATH.
type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return e.Name + ": executable not found" }

// Runner executes commands. The interface exists so the pipeline can be tested
// without invoking real tools.
type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Exec is the production Runner, backed by os/exec.
type Exec struct{}

// New returns a Runner that actually spawns processes.
func New() Runner { return Exec{} }

// Run executes spec, streaming output to spec.OnLine while capturing stdout in
// full and retaining the tail of combined output for error reporting.
//
// Cancelling ctx kills the child process; Run then returns ctx.Err() so callers
// can distinguish a deliberate cancellation from a tool failure.
func (Exec) Run(ctx context.Context, spec Spec) (Result, error) {
	if _, err := exec.LookPath(spec.Name); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Result{}, &NotFoundError{Name: spec.Name}
		}
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(cmd.Environ(), spec.Env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("%s: stdout pipe: %w", spec.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("%s: stderr pipe: %w", spec.Name, err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("%s: start: %w", spec.Name, err)
	}

	var (
		mu        sync.Mutex
		tail      = newRing(maxCapturedLines)
		stdoutBuf bytes.Buffer
		wg        sync.WaitGroup
	)

	consume := func(r io.Reader, which Stream) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		// Progress bars and long ffmpeg filter errors can exceed the default
		// 64KiB token limit; raise it so a long line is not a hard error.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		sc.Split(scanLinesAndCR)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n")
			mu.Lock()
			if which == Stdout {
				stdoutBuf.WriteString(line)
				stdoutBuf.WriteByte('\n')
			}
			if strings.TrimSpace(line) != "" {
				tail.add(line)
			}
			mu.Unlock()
			if spec.OnLine != nil {
				spec.OnLine(which, line)
			}
		}
	}

	wg.Add(2)
	go consume(stdout, Stdout)
	go consume(stderr, Stderr)
	wg.Wait()

	waitErr := cmd.Wait()

	mu.Lock()
	res := Result{Stdout: stdoutBuf.String()}
	captured := tail.slice()
	mu.Unlock()

	if waitErr != nil {
		// A cancelled context is the caller's doing, not a tool failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return res, ctxErr
		}
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, &ExitError{
				Command:  spec.String(),
				ExitCode: ee.ExitCode(),
				Output:   captured,
			}
		}
		return res, fmt.Errorf("%s: %w", spec.Name, waitErr)
	}
	return res, nil
}

// scanLinesAndCR splits on either "\n" or "\r". Progress-reporting tools
// (yt-dlp, demucs/tqdm) redraw a single line using carriage returns, so
// splitting on newlines alone would withhold every progress update until the
// process exited.
func scanLinesAndCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ring is a fixed-capacity FIFO of the most recent lines.
type ring struct {
	buf  []string
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]string, n)} }

func (r *ring) add(s string) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// slice returns the retained lines in insertion order.
func (r *ring) slice() []string {
	if !r.full {
		return append([]string(nil), r.buf[:r.next]...)
	}
	out := make([]string, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}
