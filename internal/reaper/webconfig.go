package reaper

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// REAPER's web interface is the only way in from outside, and it is off by
// default. Enabling it means a few lines in reaper.ini, which is reachable
// when REAPER is closed -- REAPER reads that file at startup and rewrites it
// on exit, so anything written while it runs is discarded.

// surfaceLine matches a control-surface entry, e.g.
// csurf_0=HTTP 0 8765 ” 'index.html' 0 ”
var surfaceLine = regexp.MustCompile(`^csurf_(\d+)=(.*)$`)

// WebConfigResult describes what enabling the interface did.
type WebConfigResult struct {
	// Path is the config file touched.
	Path string
	// Port the interface will serve on.
	Port int
	// AlreadyEnabled reports that nothing needed changing.
	AlreadyEnabled bool
	// Existing counts the control surfaces already configured, which are
	// preserved rather than replaced.
	Existing int
}

// REAPERRunningError reports that REAPER holds the config file.
type REAPERRunningError struct{}

func (e *REAPERRunningError) Error() string {
	return "REAPER is running, and it rewrites its configuration on exit, so a " +
		"change made now would be discarded.\n\n  Quit REAPER, then run this again."
}

// IsREAPERRunning reports whether REAPER currently has the config open.
func IsREAPERRunning() bool {
	out, err := exec.Command("pgrep", "-x", "REAPER").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// EnableWebInterface turns on REAPER's web remote interface on the given port.
//
// Existing control surfaces are left alone and the new entry is appended,
// because someone may already be driving REAPER from a hardware surface and
// replacing that would be a hostile thing to do quietly.
func EnableWebInterface(port int) (WebConfigResult, error) {
	res := WebConfigResult{Port: port}
	dir := ResourceDir()
	if dir == "" {
		return res, fmt.Errorf("cannot locate REAPER's configuration directory")
	}
	res.Path = filepath.Join(dir, "reaper.ini")

	if IsREAPERRunning() {
		return res, &REAPERRunningError{}
	}

	body, err := os.ReadFile(res.Path)
	if os.IsNotExist(err) {
		return res, fmt.Errorf(
			"REAPER has no configuration at %s yet; start REAPER once and quit it, then run this again",
			res.Path)
	}
	if err != nil {
		return res, fmt.Errorf("reading %s: %w", res.Path, err)
	}

	lines := strings.Split(string(body), "\n")
	highest := -1
	for _, line := range lines {
		m := surfaceLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		if n > highest {
			highest = n
		}
		res.Existing++
		// An HTTP surface already on this port needs no second copy.
		if strings.HasPrefix(m[2], "HTTP ") && strings.Contains(m[2], " "+strconv.Itoa(port)+" ") {
			res.AlreadyEnabled = true
		}
	}
	if res.AlreadyEnabled {
		return res, nil
	}

	entry := fmt.Sprintf("csurf_%d=HTTP 0 %d '' 'index.html' 0 ''", highest+1, port)
	out, err := writeSurface(lines, entry, res.Existing+1)
	if err != nil {
		return res, err
	}
	if err := os.WriteFile(res.Path, []byte(out), 0o644); err != nil {
		return res, fmt.Errorf("writing %s: %w", res.Path, err)
	}
	return res, nil
}

// writeSurface adds the entry and updates the surface count, placing both in
// the [REAPER] section where REAPER expects them.
func writeSurface(lines []string, entry string, count int) (string, error) {
	var b strings.Builder
	w := bufio.NewWriter(&b)

	wroteEntry := false
	wroteCount := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "csurf_cnt="):
			fmt.Fprintf(w, "csurf_cnt=%d\n", count)
			wroteCount = true
			continue
		case trimmed == "[REAPER]":
			fmt.Fprintln(w, line)
			fmt.Fprintln(w, entry)
			wroteEntry = true
			continue
		}
		fmt.Fprintln(w, line)
	}

	// A config without a [REAPER] section is unusual but not impossible.
	if !wroteEntry {
		fmt.Fprintln(w, "[REAPER]")
		fmt.Fprintln(w, entry)
	}
	if !wroteCount {
		fmt.Fprintf(w, "csurf_cnt=%d\n", count)
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	// Collapse the blank lines the rewrite can leave behind.
	return strings.ReplaceAll(b.String(), "\n\n\n", "\n\n"), nil
}
