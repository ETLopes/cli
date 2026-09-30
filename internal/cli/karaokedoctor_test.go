package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/karaoke/audioio"
)

// stubBackend is what a build without live audio reports.
type stubBackend struct{}

func (stubBackend) Devices() ([]audioio.Device, error) { return nil, audioio.ErrUnsupported }
func (stubBackend) Open(audioio.StreamConfig) (audioio.Stream, error) {
	return nil, audioio.ErrUnsupported
}

func TestDoctorOnAStubBuildPrintsTheMacOSOnlyNotice(t *testing.T) {
	app, _ := fxApp(t)
	app.backend = stubBackend{}
	var out bytes.Buffer
	if err := runKaraokeDoctor(context.Background(), app, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain(out.String()), "macOS-only") {
		t.Errorf("no macOS-only notice:\n%s", out.String())
	}
}

func TestDoctorListsDevicesChecksTheSetupAndReportsMissingCalibration(t *testing.T) {
	app, _ := fxApp(t)
	app.cfg.Inputs = append(app.cfg.Inputs, config.KaraokeInput{Channel: 7, Name: "Far"})
	var out bytes.Buffer
	fxMust(t, runKaraokeDoctor(context.Background(), app, &out, false))
	text := plain(out.String())
	for _, want := range []string{
		"Fake Interface (default)", "4 in, 4 out, 48000 Hz",
		"input 1 (Ana) exists", "input 2 (Bruno) exists",
		"input 7 (Far) does not exist: the device has 4 capture channels",
		"outputs 1/2 exist",
		"missing for Fake Interface at 48000 Hz, outputs 1/2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, text)
		}
	}
}

func TestDoctorMicReportsLevelsAndExplainsDigitalSilence(t *testing.T) {
	app, _ := fxApp(t)
	var out bytes.Buffer
	fxMust(t, runKaraokeDoctor(context.Background(), app, &out, true))
	if text := plain(out.String()); !strings.Contains(text, "input 1 (Ana): -") || strings.Contains(text, "Privacy & Security") {
		t.Errorf("expected levels for a live mic:\n%s", text)
	}

	app.backend = &audioio.Fake{DeviceList: []audioio.Device{{ID: "f", Name: "Fake Interface",
		CaptureChannels: 4, PlaybackChannels: 4, DefaultRate: fxRate, IsDefault: true}}}
	out.Reset()
	fxMust(t, runKaraokeDoctor(context.Background(), app, &out, true))
	text := plain(out.String())
	if !strings.Contains(text, "digital silence") || !strings.Contains(text, "Privacy & Security") ||
		!strings.Contains(text, "Microphone") {
		t.Errorf("silence not explained:\n%s", text)
	}
}
