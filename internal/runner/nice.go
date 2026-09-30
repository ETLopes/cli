package runner

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
)

// Nice wraps r so every command it runs starts at a lower CPU priority, on
// platforms that have nice(1). Demucs saturates the CPU for minutes, and live
// karaoke audio has a hard real-time deadline: without this, preparing the
// next song in the background can starve the audio callback of the current
// one. Elsewhere it returns r unchanged.
func Nice(r Runner, level int) Runner { return niceFor(runtime.GOOS, r, level) }

func niceFor(goos string, r Runner, level int) Runner {
	if goos != "darwin" && goos != "linux" {
		return r
	}
	return niced{inner: r, level: level}
}

type niced struct {
	inner Runner
	level int
}

func (n niced) Run(ctx context.Context, spec Spec) (Result, error) {
	// A tool that is not installed must still surface as a NotFoundError from
	// the inner runner; behind nice it would be an opaque exit 127. So only
	// wrap commands that resolve.
	if _, err := exec.LookPath(spec.Name); err == nil {
		spec.Args = append([]string{"-n", strconv.Itoa(n.level), spec.Name}, spec.Args...)
		spec.Name = "nice"
	}
	return n.inner.Run(ctx, spec)
}
