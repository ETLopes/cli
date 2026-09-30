//go:build !darwin || !cgo

package audioio

import (
	"errors"
	"testing"
)

func TestStubBackendReportsUnsupportedFromDevicesAndOpen(t *testing.T) {
	b := Default()
	if _, err := b.Devices(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Devices err = %v, want ErrUnsupported", err)
	}
	if _, err := b.Open(StreamConfig{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open err = %v, want ErrUnsupported", err)
	}
	if ErrUnsupported.Error() != "live audio is macOS-only for now" {
		t.Fatalf("message = %q", ErrUnsupported)
	}
}
