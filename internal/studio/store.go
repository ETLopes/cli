package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store loads and saves sessions as YAML files in a directory.
type Store struct {
	Dir string
}

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

// Path returns the file a named session lives in.
func (s *Store) Path(name string) string {
	return filepath.Join(s.Dir, sanitizeName(name)+".yaml")
}

// sanitizeName keeps a session name usable as a filename without surprising
// the user by silently renaming their session.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return strings.ToLower(b.String())
}

// Save writes a session, creating the directory if needed.
func (s *Store) Save(session *Session) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}
	data, err := session.Marshal()
	if err != nil {
		return err
	}
	path := s.Path(session.Name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Load reads a session by name.
func (s *Store) Load(name string) (*Session, error) {
	path := s.Path(name)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no session named %q (looked in %s)", name, s.Dir)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	session, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if session.Name == "" || session.Name == "untitled" {
		session.Name = name
	}
	return session, nil
}

// List returns the names of stored sessions, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.Dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(out)
	return out, nil
}

// Exists reports whether a named session is stored.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.Path(name))
	return err == nil
}

// Delete removes a stored session.
func (s *Store) Delete(name string) error {
	path := s.Path(name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no session named %q", name)
		}
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
