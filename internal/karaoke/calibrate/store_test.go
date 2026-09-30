package calibrate

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleResult() Result {
	ir := []float32{0.1, -0.333333343, float32(math.Pi), 1e-30, -7.5e5, 0}
	return Result{
		Device:     "Scarlett 2i2 USB",
		SampleRate: 48000,
		Outputs:    [2]int{1, 2},
		Time:       time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Inputs: []Input{
			{Channel: 1, DelaySamples: 412.25, DelayMs: 25.77, BulkDelay: 404, IR: ir, TailMs: 250, ResidualFloorDBFS: -62, ERLEdB: 27},
			{Channel: 2, NoEchoPath: true},
		},
	}
}

func TestStoreRoundTripPreservesTheImpulseResponseBitExactly(t *testing.T) {
	dir := t.TempDir()
	want := sampleResult()
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(dir, want.Key())
	if err != nil || !found {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if len(got.Inputs) != 2 || len(got.Inputs[0].IR) != len(want.Inputs[0].IR) {
		t.Fatalf("inputs came back as %+v", got.Inputs)
	}
	for i, v := range want.Inputs[0].IR {
		if math.Float32bits(got.Inputs[0].IR[i]) != math.Float32bits(v) {
			t.Errorf("IR[%d] = %v, want %v (bits differ)", i, got.Inputs[0].IR[i], v)
		}
	}
	if !got.Time.Equal(want.Time) || got.Inputs[0].BulkDelay != 404 || !got.Inputs[1].NoEchoPath {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestStoreNeverReturnsACalibrationForAnotherDeviceRateOrPair(t *testing.T) {
	dir := t.TempDir()
	r := sampleResult()
	if err := Save(dir, r); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]Key{
		"another device": {Device: "Other", SampleRate: 48000, Outputs: [2]int{1, 2}},
		"another rate":   {Device: r.Device, SampleRate: 44100, Outputs: [2]int{1, 2}},
		"another pair":   {Device: r.Device, SampleRate: 48000, Outputs: [2]int{3, 4}},
	} {
		if _, found, err := Load(dir, k); found || err != nil {
			t.Errorf("%s: found %v, err %v, want a clean miss", name, found, err)
		}
	}
}

func TestStoreKeysThatSanitizeToTheSameFilenameDoNotShareAResult(t *testing.T) {
	dir := t.TempDir()
	a := Key{Device: "Mic/USB", SampleRate: 48000, Outputs: [2]int{1, 2}}
	b := Key{Device: "Mic_USB", SampleRate: 48000, Outputs: [2]int{1, 2}}
	if a.filename() != b.filename() {
		t.Fatal("test premise broken: these keys should collide on the filename")
	}
	r := sampleResult()
	r.Device = a.Device
	if err := Save(dir, r); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := Load(dir, b); found {
		t.Error("a calibration of \"Mic/USB\" was returned for \"Mic_USB\"")
	}
}

func TestStoreMissingFileIsNotAnError(t *testing.T) {
	_, found, err := Load(t.TempDir(), sampleResult().Key())
	if found || err != nil {
		t.Errorf("found %v, err %v", found, err)
	}
}

func TestStoreCorruptFileIsAnErrorNotAPanic(t *testing.T) {
	dir := t.TempDir()
	r := sampleResult()
	if err := os.WriteFile(filepath.Join(dir, r.Key().filename()), []byte(`{"device": "Scar`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found, err := Load(dir, r.Key()); err == nil || found {
		t.Errorf("found %v, err %v, want an error", found, err)
	}
}

func TestStoreSaveLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		if err := Save(dir, sampleResult()); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries, want just the result", len(entries))
	}
}

func TestStaleAfterTheMaxAgeOrWhenAnInputIsMissing(t *testing.T) {
	r := sampleResult()
	now := r.Time.Add(29 * 24 * time.Hour)
	if Stale(r, now, StaleConfig{Inputs: []int{1, 2}}) {
		t.Error("29 days old with the default 30-day limit reported stale")
	}
	if !Stale(r, r.Time.Add(31*24*time.Hour), StaleConfig{Inputs: []int{1}}) {
		t.Error("31 days old was not stale")
	}
	if !Stale(r, r.Time.Add(2*time.Hour), StaleConfig{MaxAge: time.Hour, Inputs: []int{1}}) {
		t.Error("older than a custom MaxAge was not stale")
	}
	if !Stale(r, now, StaleConfig{Inputs: []int{1, 3}}) {
		t.Error("input 3 is not in the result but the calibration was not stale")
	}
}
