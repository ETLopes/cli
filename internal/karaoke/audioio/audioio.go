// Package audioio is the live audio backend for karaoke: one duplex stream that
// plays the instrumental on a chosen output pair and delivers the chosen mic
// input channels, sample-aligned with what was played.
//
// The real-time rule for everything in this package: the audio callback never
// blocks, never allocates and never takes a lock. It only copies frames between
// the device buffers, preallocated scratch and lock-free rings. All decimation,
// echo cancellation and scoring happen on other goroutines that drain the rings.
package audioio

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ErrUnsupported is returned by the stub backend on every build that has no live
// audio (anything but macOS with cgo). Callers compare with errors.Is.
var ErrUnsupported = errors.New("live audio is macOS-only for now")

// Device describes one audio interface as the backend sees it.
type Device struct {
	// ID is opaque: only the backend that produced it can interpret it.
	ID   string
	Name string
	// CaptureChannels and PlaybackChannels are 0 when the device has no such side.
	CaptureChannels  int
	PlaybackChannels int
	// DefaultRate is the device's current sample rate in Hz.
	DefaultRate int
	IsDefault   bool
}

// StreamConfig selects the device, the channels and the playback source.
type StreamConfig struct {
	// DeviceID wins over DeviceName. When both are empty the system default
	// devices are used.
	DeviceID   string
	DeviceName string
	// Inputs are 1-based capture channel numbers, one per player/mic. Capture(i)
	// returns the ring for Inputs[i], so the config order is preserved.
	Inputs []int
	// Outputs is the 1-based output pair that carries the stereo playback; every
	// other output channel is silenced.
	Outputs [2]int
	// SampleRate 0 means "use the device's current rate". The backend never asks
	// the hardware to change rate, so other software using it (a DAW) is not
	// disturbed.
	SampleRate int
	// PeriodFrames is the target callback size; about 5-10 ms is the goal.
	PeriodFrames int
	// Source supplies the playback. Nil plays silence.
	Source PlaybackSource
}

// Backend lists devices and opens duplex streams.
type Backend interface {
	Devices() ([]Device, error)
	Open(StreamConfig) (Stream, error)
}

// Stream is an open duplex stream.
type Stream interface {
	Start() error
	Stop() error
	Close() error
	// SampleRate and PeriodFrames report what was actually granted.
	SampleRate() int
	PeriodFrames() int
	// Capture returns the ring of raw mono samples, at the device rate, for
	// StreamConfig.Inputs[i].
	Capture(i int) *Ring
	// Reference returns the mono downmix (L+R)/2 of the exact samples written to
	// the output pair. Sample n of every capture ring corresponds to sample n of
	// this ring, because the callback advances all of them by the same frame count.
	Reference() *Ring
	Stats() *Stats
}

// Stats are updated by the callback and read from anywhere. Ring counts are in
// samples (frames, as every ring is mono).
type Stats struct {
	// Frames is the number of frames the callback has processed: the stream clock.
	Frames atomic.Uint64
	// CaptureOverflows counts capture samples dropped because a ring was full.
	CaptureOverflows atomic.Uint64
	// ReferenceOverflows counts reference samples dropped because the ring was full.
	ReferenceOverflows atomic.Uint64
	// SourceUnderruns counts frames a still-running source failed to supply.
	SourceUnderruns atomic.Uint64
}

// ringSeconds is how much audio each ring holds. Consumers run on a goroutine
// that wakes many times a second, so a couple of seconds only fills up when the
// consumer has stalled, which the overflow counters then report.
const ringSeconds = 2

// Validate checks a config against a device before anything is opened, so the
// user sees channel counts instead of a driver error.
func Validate(cfg StreamConfig, dev Device) error {
	if len(cfg.Inputs) == 0 {
		return errors.New("no input channels selected")
	}
	seen := map[int]bool{}
	for _, in := range cfg.Inputs {
		if in < 1 {
			return fmt.Errorf("input channel %d is invalid: channels are numbered from 1", in)
		}
		if in > dev.CaptureChannels {
			return fmt.Errorf("input channel %d is not available: %q has %d capture channels", in, dev.Name, dev.CaptureChannels)
		}
		if seen[in] {
			return fmt.Errorf("input channel %d is selected twice", in)
		}
		seen[in] = true
	}
	for _, out := range cfg.Outputs {
		if out < 1 {
			return fmt.Errorf("output channel %d is invalid: channels are numbered from 1", out)
		}
		if out > dev.PlaybackChannels {
			return fmt.Errorf("output channel %d is not available: %q has %d playback channels", out, dev.Name, dev.PlaybackChannels)
		}
	}
	if cfg.Outputs[0] == cfg.Outputs[1] {
		return fmt.Errorf("output pair %d/%d must use two different channels", cfg.Outputs[0], cfg.Outputs[1])
	}
	return nil
}

// findDevice picks the device a config names: by ID first, then by name
// (case-insensitive).
func findDevice(devs []Device, cfg StreamConfig) (Device, error) {
	if cfg.DeviceID != "" {
		for _, d := range devs {
			if d.ID == cfg.DeviceID {
				return d, nil
			}
		}
		return Device{}, fmt.Errorf("no audio device with id %q", cfg.DeviceID)
	}
	for _, d := range devs {
		if strings.EqualFold(d.Name, cfg.DeviceName) {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("no audio device named %q", cfg.DeviceName)
}
