//go:build !darwin || !cgo

package audioio

// Default returns the platform backend. On this build there is no live audio, so
// every call reports ErrUnsupported and the UI can say so instead of listing devices.
func Default() Backend { return stubBackend{} }

type stubBackend struct{}

func (stubBackend) Devices() ([]Device, error) { return nil, ErrUnsupported }

func (stubBackend) Open(StreamConfig) (Stream, error) { return nil, ErrUnsupported }
