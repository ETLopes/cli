package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ETLopes/cli/internal/karaoke/calibrate"
)

func TestCalibrateAgainstAFakeRoomSavesAndDoctorThenReportsOk(t *testing.T) {
	app, _ := fxApp(t)
	var out bytes.Buffer
	fxMust(t, runKaraokeCalibrate(context.Background(), app, &out))
	text := plain(out.String())
	for _, want := range []string{"Playing the sweep", "Analysing", "input 1: delay", "input 2: delay", "echo reduction", "calibration saved for Fake Interface at 48000 Hz"} {
		if !strings.Contains(text, want) {
			t.Errorf("calibrate output lacks %q:\n%s", want, text)
		}
	}

	var doc bytes.Buffer
	fxMust(t, runKaraokeDoctor(context.Background(), app, &doc, false))
	d := plain(doc.String())
	if !strings.Contains(d, "ok for Fake Interface at 48000 Hz, outputs 1/2") || !strings.Contains(d, "input 1: echo reduction") {
		t.Errorf("doctor does not report the fresh calibration as ok:\n%s", d)
	}
}

func TestClippingGetsAnActionableMessage(t *testing.T) {
	err := calibrateError(fmt.Errorf("input 1: %w", calibrate.ErrClipping))
	if err == nil || !strings.Contains(err.Error(), "lower the input gain") {
		t.Errorf("err = %v", err)
	}
	other := fmt.Errorf("boom")
	if calibrateError(other) != other {
		t.Error("other errors must pass through unchanged")
	}
}
