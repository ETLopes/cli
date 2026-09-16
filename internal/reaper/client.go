// Package reaper drives REAPER through its built-in web remote interface.
//
// Neither of REAPER's control channels is sufficient alone: OSC can move
// faders but cannot create tracks or assign hardware I/O, and ReaScript can do
// everything but runs inside REAPER where a Go process cannot call it. The web
// interface bridges the two -- it is reachable over HTTP, and it can read and
// write the extended state a resident ReaScript watches.
package reaper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultPort is REAPER's stock web interface port.
const DefaultPort = 8080

// DefaultTimeout bounds a single request. REAPER services the web interface on
// its main thread, so a modal dialog or a busy render can stall it; failing is
// better than hanging the caller.
const DefaultTimeout = 5 * time.Second

// Client talks to REAPER's web remote interface.
type Client struct {
	// BaseURL is the interface root, e.g. "http://127.0.0.1:8080".
	BaseURL string
	// HTTP performs requests. Replaceable for tests.
	HTTP *http.Client
}

// NewClient returns a client for a host and port.
func NewClient(host string, port int) *Client {
	if host == "" {
		host = "127.0.0.1"
	}
	return &Client{
		BaseURL: fmt.Sprintf("http://%s", net.JoinHostPort(host, fmt.Sprint(port))),
		HTTP:    &http.Client{Timeout: DefaultTimeout},
	}
}

// NotReachableError reports that REAPER's web interface could not be contacted.
// It carries setup guidance because an unreachable interface is nearly always
// a configuration gap rather than a fault.
type NotReachableError struct {
	BaseURL string
	Err     error
}

func (e *NotReachableError) Error() string {
	return fmt.Sprintf("cannot reach REAPER at %s: %v\n\n"+
		"  Check that REAPER is running, then enable:\n"+
		"    Preferences → Control/OSC/web → Add → Web browser interface",
		e.BaseURL, e.Err)
}

func (e *NotReachableError) Unwrap() error { return e.Err }

// Do sends one or more web-interface commands and returns the response lines.
//
// REAPER accepts several commands in a single request separated by semicolons
// and answers with tab-separated lines, so a batch costs one round trip.
func (c *Client) Do(ctx context.Context, commands ...string) ([]string, error) {
	if len(commands) == 0 {
		return nil, errors.New("no commands given")
	}
	endpoint := c.BaseURL + "/_/" + strings.Join(commands, ";")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// net/http wraps failures in a *url.Error carrying the whole request
		// URL, which here is a percent-escaped bridge payload. Unwrapping to
		// the cause keeps the message to the part a reader can act on.
		cause := err
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			cause = urlErr.Err
		}
		return nil, &NotReachableError{BaseURL: c.BaseURL, Err: cause}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("REAPER returned %s for %q", resp.Status, strings.Join(commands, ";"))
	}

	var lines []string
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// Info describes the connected REAPER instance.
type Info struct {
	Version string
	Project string
}

// Ping confirms REAPER is reachable and reports what it is running.
func (c *Client) Ping(ctx context.Context) (Info, error) {
	lines, err := c.Do(ctx, "TRANSPORT")
	if err != nil {
		return Info{}, err
	}
	if len(lines) == 0 {
		return Info{}, fmt.Errorf("REAPER at %s answered but said nothing; "+
			"is the web interface serving the default pages?", c.BaseURL)
	}
	return Info{Version: c.version(ctx), Project: c.project(ctx)}, nil
}

// version reads REAPER's version, tolerating its absence: an older build that
// does not answer should not make the whole connection look broken.
func (c *Client) version(ctx context.Context) string {
	lines, err := c.Do(ctx, "GET/EXTSTATE/reaper/version")
	if err != nil || len(lines) == 0 {
		return ""
	}
	return lastField(lines[0])
}

func (c *Client) project(ctx context.Context) string {
	lines, err := c.Do(ctx, "GET/PROJEXTSTATE/clistudio/project")
	if err != nil || len(lines) == 0 {
		return ""
	}
	return lastField(lines[0])
}

// GetExtState reads a persistent extended-state value.
func (c *Client) GetExtState(ctx context.Context, section, key string) (string, error) {
	lines, err := c.Do(ctx, fmt.Sprintf("GET/EXTSTATE/%s/%s", esc(section), esc(key)))
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "EXTSTATE\t") {
			return lastField(line), nil
		}
	}
	// A key that has never been set yields no line, which is not an error.
	return "", nil
}

// SetExtState writes a persistent extended-state value.
func (c *Client) SetExtState(ctx context.Context, section, key, value string) error {
	_, err := c.Do(ctx, fmt.Sprintf("SET/EXTSTATE/%s/%s/%s", esc(section), esc(key), esc(value)))
	return err
}

// RunAction triggers a REAPER action by ID, numeric or named.
func (c *Client) RunAction(ctx context.Context, action string) error {
	_, err := c.Do(ctx, esc(action))
	return err
}

// esc percent-encodes a path segment. Values reaching here can contain
// slashes and spaces, either of which would otherwise be read as command
// structure rather than data.
func esc(s string) string { return url.PathEscape(s) }

// lastField returns the final tab-separated field of a response line, which is
// where the web interface puts the value.
func lastField(line string) string {
	parts := strings.Split(line, "\t")
	return parts[len(parts)-1]
}
