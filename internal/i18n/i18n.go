// Package i18n renders the application's text in the user's language.
//
// Messages are looked up by key rather than by translating English at the
// point of use, so a missing translation is visible as a key at review time
// instead of silently falling back to the wrong language mid-sentence.
package i18n

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Lang identifies a language.
type Lang string

// Supported languages.
const (
	EN Lang = "en"
	PT Lang = "pt"
)

// Supported lists the languages the application speaks.
func Supported() []Lang { return []Lang{EN, PT} }

// Name is the language's own name for itself, which is what a language picker
// should show: someone looking for Portuguese is looking for "Português".
func (l Lang) Name() string {
	switch l {
	case PT:
		return "Português"
	default:
		return "English"
	}
}

// Parse reads a language tag, accepting the regional forms people actually
// type: pt, pt-BR, pt_BR.
func Parse(s string) (Lang, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "_-."); i > 0 {
		s = s[:i]
	}
	switch s {
	case "en":
		return EN, true
	case "pt":
		return PT, true
	}
	return EN, false
}

// Detect reads the language from the environment, which is what a user who has
// already told their system they speak Portuguese would expect.
func Detect() Lang {
	for _, key := range []string{"CLI_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(key); v != "" {
			if l, ok := Parse(v); ok {
				return l
			}
		}
	}
	return EN
}

var (
	mu      sync.RWMutex
	current = EN
)

// Use sets the language for subsequent lookups.
func Use(l Lang) {
	mu.Lock()
	defer mu.Unlock()
	current = l
}

// Current returns the language in force.
func Current() Lang {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// catalogs holds every translation, keyed by language then by message key.
var catalogs = map[Lang]map[string]string{
	EN: en,
	PT: pt,
}

// T returns the message for a key.
//
// An untranslated key falls back to English rather than to the key itself: a
// user reading a half-translated screen is better served by a sentence in the
// wrong language than by "console.help.comp".
func T(key string) string {
	mu.RLock()
	lang := current
	mu.RUnlock()

	if c, ok := catalogs[lang]; ok {
		if s, ok := c[key]; ok && s != "" {
			return s
		}
	}
	if s, ok := en[key]; ok {
		return s
	}
	return key
}

// Tf is T with formatting.
func Tf(key string, args ...any) string { return fmt.Sprintf(T(key), args...) }

// Missing lists keys present in English but absent from another language, so a
// test can report what still needs translating rather than leaving it to be
// noticed in use.
func Missing(l Lang) []string {
	c, ok := catalogs[l]
	if !ok {
		return nil
	}
	var out []string
	for key := range en {
		if s, found := c[key]; !found || s == "" {
			out = append(out, key)
		}
	}
	return out
}

// Keys lists every message key.
func Keys() []string {
	out := make([]string, 0, len(en))
	for k := range en {
		out = append(out, k)
	}
	return out
}
