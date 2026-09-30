//go:build darwin && cgo

package audioio

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// Default returns the CoreAudio backend, built on miniaudio through malgo.
func Default() Backend { return malgoBackend{} }

type malgoBackend struct{}

// side is one direction (capture or playback) of a device as miniaudio lists it.
// CoreAudio reports the two directions of an interface as separate entries with
// the same name.
type side struct {
	name      string
	id        malgo.DeviceID
	channels  int
	rate      int
	isDefault bool
}

func newContext() (*malgo.AllocatedContext, error) {
	ctx, err := malgo.InitContext([]malgo.Backend{malgo.BackendCoreaudio}, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("start CoreAudio: %w", err)
	}
	return ctx, nil
}

func closeContext(ctx *malgo.AllocatedContext) {
	_ = ctx.Uninit()
	ctx.Free()
}

// sides lists one direction, querying full device info because enumeration alone
// carries no channel counts.
func sides(ctx *malgo.AllocatedContext, kind malgo.DeviceType) ([]side, error) {
	infos, err := ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("list audio devices: %w", err)
	}
	out := make([]side, 0, len(infos))
	for _, in := range infos {
		full, err := ctx.DeviceInfo(kind, in.ID, malgo.Shared)
		if err != nil {
			full = in // keep the device listed, with unknown channel counts
		}
		s := side{name: in.Name(), id: in.ID, isDefault: in.IsDefault != 0}
		for _, f := range full.Formats {
			s.channels = max(s.channels, int(f.Channels))
			if s.rate == 0 {
				s.rate = int(f.SampleRate)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// encodeID packs the two directions' IDs into the opaque Device.ID.
func encodeID(capture, playback *side) string {
	enc := func(s *side) string {
		if s == nil {
			return ""
		}
		return s.id.String() // hex with trailing zero bytes trimmed
	}
	return "ca:" + enc(capture) + "|" + enc(playback)
}

func decodeID(id string) (capture, playback *malgo.DeviceID, err error) {
	rest, ok := strings.CutPrefix(id, "ca:")
	c, p, ok2 := strings.Cut(rest, "|")
	if !ok || !ok2 {
		return nil, nil, fmt.Errorf("malformed audio device id %q", id)
	}
	dec := func(h string) (*malgo.DeviceID, error) {
		if h == "" {
			return nil, nil
		}
		b, err := hex.DecodeString(h)
		var out malgo.DeviceID
		if err != nil || len(b) > len(out) {
			return nil, fmt.Errorf("malformed audio device id %q", id)
		}
		copy(out[:], b)
		return &out, nil
	}
	if capture, err = dec(c); err != nil {
		return nil, nil, err
	}
	if playback, err = dec(p); err != nil {
		return nil, nil, err
	}
	return capture, playback, nil
}

// merge pairs capture and playback entries of the same name into Devices.
func merge(caps, plays []side) []Device {
	var devs []Device
	used := map[int]bool{}
	for pi := range plays {
		p := &plays[pi]
		var c *side
		for ci := range caps {
			if !used[ci] && caps[ci].name == p.name {
				used[ci], c = true, &caps[ci]
				break
			}
		}
		d := Device{ID: encodeID(c, p), Name: p.name, PlaybackChannels: p.channels, DefaultRate: p.rate, IsDefault: p.isDefault}
		if c != nil {
			d.CaptureChannels = c.channels
			d.IsDefault = d.IsDefault || c.isDefault
		}
		devs = append(devs, d)
	}
	for ci := range caps {
		if used[ci] {
			continue
		}
		c := &caps[ci]
		devs = append(devs, Device{ID: encodeID(c, nil), Name: c.name, CaptureChannels: c.channels, DefaultRate: c.rate, IsDefault: c.isDefault})
	}
	return devs
}

func (malgoBackend) Devices() ([]Device, error) {
	ctx, err := newContext()
	if err != nil {
		return nil, err
	}
	defer closeContext(ctx)
	caps, err := sides(ctx, malgo.Capture)
	if err != nil {
		return nil, err
	}
	plays, err := sides(ctx, malgo.Playback)
	if err != nil {
		return nil, err
	}
	return merge(caps, plays), nil
}

// resolve finds the device a config names. With no ID and no name it returns the
// system default capture and playback (nil IDs make miniaudio use the defaults).
func resolve(ctx *malgo.AllocatedContext, cfg StreamConfig) (dev Device, capID, playID *malgo.DeviceID, err error) {
	caps, err := sides(ctx, malgo.Capture)
	if err != nil {
		return
	}
	plays, err := sides(ctx, malgo.Playback)
	if err != nil {
		return
	}
	if cfg.DeviceID == "" && cfg.DeviceName == "" {
		dev = Device{Name: "default audio devices", IsDefault: true}
		for _, c := range caps {
			if c.isDefault {
				dev.CaptureChannels, dev.DefaultRate = c.channels, c.rate
			}
		}
		for _, p := range plays {
			if p.isDefault {
				dev.PlaybackChannels = p.channels
				dev.DefaultRate = p.rate
			}
		}
		return
	}
	dev, err = findDevice(merge(caps, plays), cfg)
	if err != nil {
		return
	}
	capID, playID, err = decodeID(dev.ID)
	return
}

// f32 views a device byte buffer as float32 samples without copying or allocating.
func f32(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), len(b)/4)
}

func (malgoBackend) Open(cfg StreamConfig) (Stream, error) {
	ctx, err := newContext()
	if err != nil {
		return nil, err
	}
	dev, capID, playID, err := resolve(ctx, cfg)
	if err == nil {
		err = Validate(cfg, dev)
	}
	if err != nil {
		closeContext(ctx)
		return nil, fmt.Errorf("open audio stream: %w", err)
	}

	e := newEngine(cfg, max(cfg.SampleRate, dev.DefaultRate, 48000))
	s := &malgoStream{ctx: ctx, e: e}

	dc := malgo.DefaultDeviceConfig(malgo.Duplex)
	dc.SampleRate = uint32(cfg.SampleRate) // 0: keep the device's current rate
	dc.PeriodSizeInFrames = uint32(cfg.PeriodFrames)
	dc.PerformanceProfile = malgo.LowLatency
	dc.Capture.Format, dc.Playback.Format = malgo.FormatF32, malgo.FormatF32
	dc.Capture.Channels, dc.Playback.Channels = uint32(e.inCh), uint32(e.outCh)
	dc.Capture.ShareMode, dc.Playback.ShareMode = malgo.Shared, malgo.Shared
	// miniaudio copies the IDs during init, so C copies freed right after are fine.
	// They must be C memory: Go pointers inside the config would trip cgocheck.
	for _, p := range []struct {
		id  *malgo.DeviceID
		dst *unsafe.Pointer
	}{{capID, &dc.Capture.DeviceID}, {playID, &dc.Playback.DeviceID}} {
		if p.id != nil {
			ptr := p.id.Pointer()
			defer C.free(ptr)
			*p.dst = ptr
		}
	}

	d, err := malgo.InitDevice(ctx.Context, dc, malgo.DeviceCallbacks{Data: s.callback})
	if err != nil {
		closeContext(ctx)
		return nil, fmt.Errorf("open audio stream on %q: %w", dev.Name, err)
	}
	s.dev = d
	s.rate = int(d.SampleRate())
	s.requested = cfg.PeriodFrames
	return s, nil
}

type malgoStream struct {
	ctx       *malgo.AllocatedContext
	dev       *malgo.Device
	e         *engine
	rate      int
	requested int
	observed  atomic.Uint32 // callback size seen on the audio thread
	closed    bool
}

// callback is the audio thread body. It is lock-free and allocation-free here;
// malgo itself takes a short mutex to look the callback up, which is outside our
// control. It must never panic, so short buffers are ignored rather than indexed.
func (s *malgoStream) callback(out, in []byte, frames uint32) {
	o, i, n := f32(out), f32(in), int(frames)
	if len(o) < n*s.e.outCh {
		return
	}
	s.observed.Store(frames)
	s.e.process(o, i, n)
}

func (s *malgoStream) Start() error {
	if s.closed {
		return errors.New("audio stream is closed")
	}
	return s.dev.Start()
}

func (s *malgoStream) Stop() error { return s.dev.Stop() }

func (s *malgoStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.dev.Uninit()
	closeContext(s.ctx)
	return nil
}

func (s *malgoStream) SampleRate() int { return s.rate }

// PeriodFrames is the callback size observed once the device has run, and the
// requested size before that (miniaudio does not expose the granted period).
func (s *malgoStream) PeriodFrames() int {
	if n := s.observed.Load(); n > 0 {
		return int(n)
	}
	return s.requested
}

func (s *malgoStream) Capture(i int) *Ring { return s.e.capture[i] }
func (s *malgoStream) Reference() *Ring    { return s.e.ref }
func (s *malgoStream) Stats() *Stats       { return &s.e.stats }
