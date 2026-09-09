// Package config resolves settings from, in increasing order of precedence:
// built-in defaults, a config file, DTX_-prefixed environment variables, and
// command-line flags.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"

	"github.com/eduardolopes/dtx/internal/separate"
)

// EnvPrefix namespaces the environment variables this program reads, so
// DTX_MODEL sets the Demucs model.
const EnvPrefix = "DTX"

// FileName is the config file's base name; Viper appends a supported extension.
const FileName = "config"

// Config holds every user-tunable setting.
type Config struct {
	// OutputDir is the root under which per-track folders are created.
	OutputDir string `mapstructure:"output_dir" yaml:"output_dir" validate:"required"`
	// Model is the Demucs model name.
	Model string `mapstructure:"model" yaml:"model" validate:"required"`
	// Device is the Demucs compute backend.
	Device string `mapstructure:"device" yaml:"device" validate:"required,oneof=auto cpu mps cuda"`
	// Shifts trades time for separation quality.
	Shifts int `mapstructure:"shifts" yaml:"shifts" validate:"gte=0,lte=10"`
	// Jobs is the number of parallel Demucs workers; 0 lets Demucs decide.
	Jobs int `mapstructure:"jobs" yaml:"jobs" validate:"gte=0,lte=32"`
	// Normalize applies loudness normalization to rendered mixes.
	Normalize bool `mapstructure:"normalize" yaml:"normalize"`
	// Limit applies a brickwall limiter to rendered mixes.
	Limit bool `mapstructure:"limit" yaml:"limit"`
	// USBPath is a drive to copy finished files onto.
	USBPath string `mapstructure:"usb_path" yaml:"usb_path"`
	// CookiesFromBrowser lets yt-dlp borrow cookies for restricted videos.
	CookiesFromBrowser string `mapstructure:"cookies_from_browser" yaml:"cookies_from_browser" validate:"omitempty,oneof=chrome chromium firefox safari edge brave opera vivaldi"`
}

// KnownModels are the Demucs models the CLI advertises. Others still work if
// named explicitly; this list drives the interactive picker and help text.
var KnownModels = []string{separate.ModelDefault, separate.ModelFineTuned, separate.ModelSixStem}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		OutputDir: defaultOutputDir(),
		Model:     separate.ModelDefault,
		Device:    separate.DeviceAuto,
	}
}

// defaultOutputDir puts results somewhere obvious rather than in the current
// working directory, which is rarely where a user wants several hundred
// megabytes of audio to land.
func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dtx-out"
	}
	return filepath.Join(home, "Music", "dtx")
}

// Dir is the directory holding the config file.
func Dir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "dtx")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".config", "dtx")
}

// Bind wires defaults and environment handling into v. Flags are bound
// separately by the command layer, which owns the flag definitions.
func Bind(v *viper.Viper) {
	d := Defaults()
	v.SetDefault("output_dir", d.OutputDir)
	v.SetDefault("model", d.Model)
	v.SetDefault("device", d.Device)
	v.SetDefault("shifts", d.Shifts)
	v.SetDefault("jobs", d.Jobs)
	v.SetDefault("normalize", d.Normalize)
	v.SetDefault("limit", d.Limit)
	v.SetDefault("usb_path", d.USBPath)
	v.SetDefault("cookies_from_browser", d.CookiesFromBrowser)

	v.SetConfigName(FileName)
	v.AddConfigPath(Dir())
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
}

// Load reads configuration from v. A missing config file is not an error --
// the program is expected to work with no configuration at all.
func Load(v *viper.Viper) (Config, error) {
	if err := v.ReadInConfig(); err != nil {
		// Running with no config file at all is the normal case, not a failure.
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("reading config file: %w", err)
		}
	}

	cfg := Defaults()
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	// Expand a leading "~" so a hand-edited config file behaves as expected.
	cfg.OutputDir = expandHome(cfg.OutputDir)
	cfg.USBPath = expandHome(cfg.USBPath)
	return cfg, nil
}

// Validate checks the configuration, translating validator's output into
// messages that name the setting a user would actually edit.
func (c Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())
	err := v.Struct(c)
	if err == nil {
		return nil
	}

	// A malformed struct is a programming error, not a user-facing one.
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return fmt.Errorf("validating config: %w", err)
	}

	var fieldErrs validator.ValidationErrors
	if !errors.As(err, &fieldErrs) {
		return fmt.Errorf("validating config: %w", err)
	}

	msgs := make([]string, 0, len(fieldErrs))
	for _, fe := range fieldErrs {
		msgs = append(msgs, describe(fe))
	}
	return fmt.Errorf("invalid configuration: %s", strings.Join(msgs, "; "))
}

// describe renders one validation failure using the setting's config-file name.
func describe(fe validator.FieldError) string {
	name := settingName(fe.Field())
	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s must be set", name)
	case "oneof":
		return fmt.Sprintf("%s must be one of: %s (got %q)", name, strings.ReplaceAll(fe.Param(), " ", ", "), fe.Value())
	case "gte":
		return fmt.Sprintf("%s must be at least %s (got %v)", name, fe.Param(), fe.Value())
	case "lte":
		return fmt.Sprintf("%s must be at most %s (got %v)", name, fe.Param(), fe.Value())
	default:
		return fmt.Sprintf("%s is invalid (got %v)", name, fe.Value())
	}
}

// settingName converts a Go field name to the snake_case key users see.
func settingName(field string) string {
	var b strings.Builder
	for i, r := range field {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func expandHome(path string) string {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
