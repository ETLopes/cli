// Package runnertest provides a fake command runner for tests, so the pipeline
// can be exercised end to end without yt-dlp, ffmpeg or Demucs installed.
package runnertest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/eduardolopes/dtx/internal/runner"
)

// Call records one command invocation.
type Call struct {
	Name string
	Args []string
}

// HasArg reports whether the call included the given argument.
func (c Call) HasArg(want string) bool {
	for _, a := range c.Args {
		if a == want {
			return true
		}
	}
	return false
}

// ArgAfter returns the argument following flag, which is how most of the tools
// here take their values.
func (c Call) ArgAfter(flag string) (string, bool) {
	for i, a := range c.Args {
		if a == flag && i+1 < len(c.Args) {
			return c.Args[i+1], true
		}
	}
	return "", false
}

// String renders the call as it would be typed.
func (c Call) String() string { return c.Name + " " + strings.Join(c.Args, " ") }

// Response is what the fake returns for a matched command.
type Response struct {
	Stdout string
	// Lines are delivered to the spec's OnLine callback before returning,
	// which is how progress parsing gets exercised.
	Lines []string
	// Stream is the pipe Lines arrive on. runner.Stdout is the zero value and
	// the default, matching ffmpeg's "-progress pipe:1"; set it explicitly to
	// runner.Stderr for tools like demucs that report progress there.
	Stream runner.Stream
	Err    error
	// Do runs side effects, such as creating the files a real tool would
	// have written.
	Do func(spec runner.Spec) error
}

// Fake is a runner.Runner that answers from a set of registered handlers.
type Fake struct {
	mu    sync.Mutex
	calls []Call

	// Handlers are consulted in order; the first whose match function returns
	// true supplies the response.
	handlers []handler
	// Default is returned when no handler matches. When nil, an unmatched
	// command fails the way an unexpected call should.
	Default *Response
}

type handler struct {
	match func(runner.Spec) bool
	resp  func(runner.Spec) Response
}

// New returns an empty Fake.
func New() *Fake { return &Fake{} }

// Handle registers a response for commands whose executable name contains name
// and whose arguments contain every string in mustHaveArgs.
func (f *Fake) Handle(name string, mustHaveArgs []string, resp Response) *Fake {
	return f.HandleFunc(name, mustHaveArgs, func(runner.Spec) Response { return resp })
}

// HandleFunc is Handle with a response computed per call.
func (f *Fake) HandleFunc(name string, mustHaveArgs []string, fn func(runner.Spec) Response) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, handler{
		match: func(s runner.Spec) bool {
			if !strings.Contains(s.Name, name) {
				return false
			}
			for _, want := range mustHaveArgs {
				if !containsArg(s.Args, want) {
					return false
				}
			}
			return true
		},
		resp: fn,
	})
	return f
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// Run implements runner.Runner.
func (f *Fake) Run(ctx context.Context, spec runner.Spec) (runner.Result, error) {
	if err := ctx.Err(); err != nil {
		return runner.Result{}, err
	}

	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: spec.Name, Args: append([]string(nil), spec.Args...)})
	var resp *Response
	for _, h := range f.handlers {
		if h.match(spec) {
			r := h.resp(spec)
			resp = &r
			break
		}
	}
	if resp == nil {
		resp = f.Default
	}
	f.mu.Unlock()

	if resp == nil {
		return runner.Result{}, fmt.Errorf("runnertest: unexpected command: %s %s", spec.Name, strings.Join(spec.Args, " "))
	}
	if resp.Do != nil {
		if err := resp.Do(spec); err != nil {
			return runner.Result{}, err
		}
	}
	if spec.OnLine != nil {
		for _, l := range resp.Lines {
			spec.OnLine(resp.Stream, l)
		}
	}
	return runner.Result{Stdout: resp.Stdout}, resp.Err
}

// Calls returns every command run so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallsTo returns the calls whose executable name contains name.
func (f *Fake) CallsTo(name string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if strings.Contains(c.Name, name) {
			out = append(out, c)
		}
	}
	return out
}
