package reaper

import (
	"strings"
	"testing"
)

// Someone may already drive REAPER from a hardware control surface. Enabling
// the web interface must add to that, never quietly replace it.
func TestWritingASurfaceKeepsTheExistingOnes(t *testing.T) {
	existing := []string{
		"[REAPER]",
		"csurf_0=MCU 0 1 0 0",
		"csurf_cnt=1",
		"someothersetting=1",
	}
	out, err := writeSurface(existing, "csurf_1=HTTP 0 8765 '' 'index.html' 0 ''", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "csurf_0=MCU 0 1 0 0") {
		t.Error("the existing control surface was lost")
	}
	if !strings.Contains(out, "csurf_1=HTTP 0 8765") {
		t.Error("the web interface entry was not written")
	}
	if !strings.Contains(out, "csurf_cnt=2") {
		t.Errorf("surface count was not updated:\n%s", out)
	}
	if strings.Count(out, "csurf_cnt=") != 1 {
		t.Errorf("surface count written more than once:\n%s", out)
	}
	if !strings.Contains(out, "someothersetting=1") {
		t.Error("an unrelated setting was lost")
	}
}

// A config with no surfaces at all is the normal fresh-install case.
func TestWritingASurfaceIntoAFreshConfig(t *testing.T) {
	out, err := writeSurface([]string{"[REAPER]", "version=7.80"},
		"csurf_0=HTTP 0 8765 '' 'index.html' 0 ''", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"[REAPER]", "csurf_0=HTTP 0 8765", "csurf_cnt=1", "version=7.80"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// The entry belongs inside the section REAPER reads it from.
	sectionAt := strings.Index(out, "[REAPER]")
	entryAt := strings.Index(out, "csurf_0=")
	if sectionAt < 0 || entryAt < sectionAt {
		t.Errorf("the surface entry is outside the [REAPER] section:\n%s", out)
	}
}

// A config without the section header should still end up valid.
func TestWritingASurfaceWithNoSection(t *testing.T) {
	out, err := writeSurface([]string{"stray=1"}, "csurf_0=HTTP 0 8765 '' 'index.html' 0 ''", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[REAPER]") || !strings.Contains(out, "csurf_0=HTTP") {
		t.Errorf("expected the section to be created:\n%s", out)
	}
}
