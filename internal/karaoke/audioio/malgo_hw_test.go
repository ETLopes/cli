//go:build darwin && cgo

package audioio

import (
	"math"
	"os"
	"testing"
	"time"
)

// TestHardwarePlaysAClickAndCapturesItOnInput1 needs a real interface with an
// output looped back (or acoustically coupled) to capture channel 1, so it only
// runs with KARAOKE_HW=1.
func TestHardwarePlaysAClickAndCapturesItOnInput1(t *testing.T) {
	if os.Getenv("KARAOKE_HW") != "1" {
		t.Skip("set KARAOKE_HW=1 to run against real audio hardware")
	}
	b := Default()
	devs, err := b.Devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		t.Logf("device %q capture=%d playback=%d rate=%d default=%v", d.Name, d.CaptureChannels, d.PlaybackChannels, d.DefaultRate, d.IsDefault)
	}

	// Two seconds of 1 kHz tone at the device's own rate, quiet enough to be safe.
	cfg := StreamConfig{Inputs: []int{1}, Outputs: [2]int{1, 2}, PeriodFrames: 480}
	if name := os.Getenv("KARAOKE_HW_DEVICE"); name != "" {
		cfg.DeviceName = name
	}
	probe, err := b.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rate := probe.SampleRate()
	probe.Close()

	tone := make([]float32, 2*2*rate)
	for i := 0; i < len(tone)/2; i++ {
		v := float32(0.1 * math.Sin(2*math.Pi*1000*float64(i)/float64(rate)))
		tone[2*i], tone[2*i+1] = v, v
	}
	cfg.Source = NewBufferSource(tone)
	s, err := b.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	s.Stop()

	t.Logf("granted rate %d period %d, frames %d", s.SampleRate(), s.PeriodFrames(), s.Stats().Frames.Load())
	if s.Stats().Frames.Load() == 0 {
		t.Fatal("callback never ran")
	}
	mic := drain(s.Capture(0))
	var energy float64
	for _, v := range mic {
		energy += float64(v) * float64(v)
	}
	if rms := math.Sqrt(energy / float64(max(len(mic), 1))); rms < 0.001 {
		t.Fatalf("capture channel 1 is silent (rms %g): is output 1/2 patched to input 1?", rms)
	}
}
