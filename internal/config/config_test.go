package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/eduardolopes/dtx/internal/audio"
	"github.com/eduardolopes/dtx/internal/separate"
)

// newViper returns a Viper rooted at an isolated config directory.
func newViper(t *testing.T) *viper.Viper {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := viper.New()
	Bind(v)
	return v
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithNoConfigFileUsesDefaults(t *testing.T) {
	v := newViper(t)
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if cfg.Model != separate.ModelDefault {
		t.Errorf("Model = %q, want %q", cfg.Model, separate.ModelDefault)
	}
	if cfg.Device != separate.DeviceAuto {
		t.Errorf("Device = %q, want %q", cfg.Device, separate.DeviceAuto)
	}
	if cfg.OutputDir == "" {
		t.Error("OutputDir should have a default")
	}
}

func TestLoadReadsConfigFile(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "model: htdemucs_ft\nshifts: 2\nnormalize: true\n")

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model != separate.ModelFineTuned {
		t.Errorf("Model = %q, want %q", cfg.Model, separate.ModelFineTuned)
	}
	if cfg.Shifts != 2 {
		t.Errorf("Shifts = %d, want 2", cfg.Shifts)
	}
	if !cfg.Normalize {
		t.Error("Normalize should be true")
	}
}

func TestEnvironmentOverridesConfigFile(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "model: htdemucs\n")
	t.Setenv("DTX_MODEL", separate.ModelSixStem)

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model != separate.ModelSixStem {
		t.Errorf("Model = %q, want the environment to win", cfg.Model)
	}
}

// Flags are applied by the command layer with v.Set, which must outrank
// both the file and the environment.
func TestExplicitSetOverridesEverything(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "model: htdemucs\n")
	t.Setenv("DTX_MODEL", separate.ModelSixStem)
	v.Set("model", separate.ModelFineTuned)

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model != separate.ModelFineTuned {
		t.Errorf("Model = %q, want an explicit Set to win", cfg.Model)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"unknown device", "device: banana\n", "device must be one of"},
		{"negative shifts", "shifts: -1\n", "shifts must be at least 0"},
		{"too many shifts", "shifts: 99\n", "shifts must be at most 10"},
		{"unknown browser", "cookies_from_browser: netscape\n", "cookies_from_browser must be one of"},
		{"empty output dir", `output_dir: ""` + "\n", "output_dir must be set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newViper(t)
			writeConfig(t, tt.body)

			_, err := Load(v)
			if err == nil {
				t.Fatalf("expected %s to be rejected", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "model: [unclosed\n")

	if _, err := Load(v); err == nil {
		t.Fatal("expected malformed YAML to be reported")
	}
}

func TestTildeInPathsIsExpanded(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "output_dir: ~/Music/somewhere\n")

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasPrefix(cfg.OutputDir, "~") {
		t.Errorf("OutputDir = %q, want the tilde expanded", cfg.OutputDir)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "Music", "somewhere"); cfg.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, want)
	}
}

// A config file round-trips: what `dtx config init` writes must load back.
func TestDefaultsRoundTripThroughYAML(t *testing.T) {
	v := newViper(t)
	writeConfig(t, `output_dir: /tmp/out
model: htdemucs_ft
device: cpu
shifts: 1
jobs: 2
normalize: true
limit: true
usb_path: /Volumes/DTX
cookies_from_browser: firefox
`)
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OutputDir != "/tmp/out" || cfg.Device != "cpu" || cfg.Jobs != 2 ||
		!cfg.Limit || cfg.USBPath != "/Volumes/DTX" || cfg.CookiesFromBrowser != "firefox" {
		t.Errorf("round-trip lost values: %+v", cfg)
	}
}

func TestValidateNamesTheSettingNotTheGoField(t *testing.T) {
	cfg := Defaults()
	cfg.CookiesFromBrowser = "netscape"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	// Users edit "cookies_from_browser", not "CookiesFromBrowser".
	if !strings.Contains(err.Error(), "cookies_from_browser") {
		t.Errorf("error = %q, want it to name the config key", err)
	}
}

func TestFormatsDefaultAndOverride(t *testing.T) {
	v := newViper(t)
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// FLAC is the safe default extra: the only lossless option, half the size.
	if len(cfg.Formats) != 1 || cfg.Formats[0] != audio.EncodingFLAC {
		t.Errorf("default Formats = %v, want [%s]", cfg.Formats, audio.EncodingFLAC)
	}

	v2 := newViper(t)
	writeConfig(t, "formats:\n  - opus\n  - mp3\n")
	cfg2, err := Load(v2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(cfg2.Formats, ",") != "opus,mp3" {
		t.Errorf("Formats = %v, want [opus mp3]", cfg2.Formats)
	}
}

func TestFormatsRejectsUnknownValue(t *testing.T) {
	v := newViper(t)
	writeConfig(t, "formats:\n  - flac\n  - wma\n")

	_, err := Load(v)
	if err == nil {
		t.Fatal("expected an unknown format to be rejected")
	}
	if !strings.Contains(err.Error(), "wma") {
		t.Errorf("error = %q, want it to name the offending value", err)
	}
	if !strings.Contains(err.Error(), "formats") {
		t.Errorf("error = %q, want it to name the setting", err)
	}
}

func TestEveryKnownFormatValidates(t *testing.T) {
	for _, id := range audio.EncodingIDs() {
		cfg := Defaults()
		cfg.Formats = []string{id}
		if err := cfg.Validate(); err != nil {
			t.Errorf("format %q should be valid, got: %v", id, err)
		}
	}
}
