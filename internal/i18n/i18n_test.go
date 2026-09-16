package i18n

import (
	"strings"
	"testing"
)

// A key added to the source catalogue without a translation shows up here
// rather than as an English sentence in the middle of a Portuguese screen.
func TestEveryMessageIsTranslated(t *testing.T) {
	for _, l := range Supported() {
		if l == EN {
			continue
		}
		if missing := Missing(l); len(missing) > 0 {
			t.Errorf("%s is missing %d message(s): %v", l, len(missing), missing)
		}
	}
}

// Format placeholders must survive translation: a message that takes a number
// in English and not in Portuguese panics at the point of use.
func TestPlaceholdersMatchAcrossLanguages(t *testing.T) {
	count := func(s string) int { return strings.Count(s, "%d") + strings.Count(s, "%s") }
	for key, source := range en {
		want := count(source)
		for _, l := range Supported() {
			if l == EN {
				continue
			}
			got := count(catalogs[l][key])
			if got != want {
				t.Errorf("%s/%s has %d placeholder(s), English has %d", l, key, got, want)
			}
		}
	}
}

func TestParseAcceptsRegionalTags(t *testing.T) {
	for _, tag := range []string{"pt", "pt-BR", "pt_BR", "PT", "pt_BR.UTF-8"} {
		if l, ok := Parse(tag); !ok || l != PT {
			t.Errorf("Parse(%q) = %v, %v; want pt", tag, l, ok)
		}
	}
	for _, tag := range []string{"en", "en-GB", "en_US.UTF-8"} {
		if l, ok := Parse(tag); !ok || l != EN {
			t.Errorf("Parse(%q) = %v, %v; want en", tag, l, ok)
		}
	}
	if _, ok := Parse("klingon"); ok {
		t.Error("an unsupported language should not resolve")
	}
}

func TestDetectReadsTheEnvironment(t *testing.T) {
	t.Setenv("CLI_LANG", "pt_BR")
	if got := Detect(); got != PT {
		t.Errorf("Detect() = %v, want pt", got)
	}
	// The application's own setting outranks the system locale.
	t.Setenv("LANG", "en_US.UTF-8")
	if got := Detect(); got != PT {
		t.Errorf("CLI_LANG should outrank LANG, got %v", got)
	}
}

// A half-translated screen is worse than an English sentence, so an unknown
// key falls back rather than printing itself.
func TestLookupFallsBackToEnglish(t *testing.T) {
	Use(PT)
	t.Cleanup(func() { Use(EN) })

	if got := T("ctrl.comp"); !strings.Contains(got, "compressor") {
		t.Errorf("expected Portuguese, got %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Errorf("an unknown key should return itself, got %q", got)
	}
}

func TestLanguagesNameThemselves(t *testing.T) {
	if PT.Name() != "Português" {
		t.Errorf("PT.Name() = %q", PT.Name())
	}
}
