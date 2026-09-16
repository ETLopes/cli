package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/ETLopes/cli/internal/reaper"
)

// StudioSection is the config key the studio tool's settings live under.
const StudioSection = "studio"

// Studio config keys.
const (
	KeyReaperHost = StudioSection + ".reaper_host"
	KeyReaperPort = StudioSection + ".reaper_port"
	KeySessionDir = StudioSection + ".session_dir"
	KeySession    = StudioSection + ".session"
)

// Studio holds the studio tool's settings.
type Studio struct {
	// ReaperHost is where REAPER's web interface listens.
	ReaperHost string `mapstructure:"reaper_host" yaml:"reaper_host" validate:"required"`
	// ReaperPort is that interface's port.
	ReaperPort int `mapstructure:"reaper_port" yaml:"reaper_port" validate:"required,min=1,max=65535"`
	// SessionDir is where session files are kept.
	SessionDir string `mapstructure:"session_dir" yaml:"session_dir" validate:"required"`
	// Session is the session opened by default.
	Session string `mapstructure:"session" yaml:"session" validate:"required"`
}

// StudioDefaults returns the built-in studio configuration.
func StudioDefaults() Studio {
	return Studio{
		// Loopback rather than a wildcard: REAPER's web interface has no
		// authentication by default, and there is no reason for anything off
		// this machine to be able to move the monitor fader.
		ReaperHost: "127.0.0.1",
		ReaperPort: reaper.DefaultPort,
		SessionDir: defaultSessionDir(),
		Session:    "default",
	}
}

func defaultSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "studio-sessions"
	}
	return filepath.Join(home, ".local", "share", AppName, "studio", "sessions")
}

// BindStudio registers the studio tool's defaults and environment bindings.
func BindStudio(v *viper.Viper) {
	d := StudioDefaults()
	defaults := map[string]any{
		KeyReaperHost: d.ReaperHost,
		KeyReaperPort: d.ReaperPort,
		KeySessionDir: d.SessionDir,
		KeySession:    d.Session,
	}
	for key, val := range defaults {
		v.SetDefault(key, val)
		_ = v.BindEnv(key)
	}
}

// LoadStudio reads the studio configuration from v.
func LoadStudio(v *viper.Viper) (Studio, error) {
	cfg := Studio{
		ReaperHost: v.GetString(KeyReaperHost),
		ReaperPort: v.GetInt(KeyReaperPort),
		SessionDir: expandHome(v.GetString(KeySessionDir)),
		Session:    v.GetString(KeySession),
	}
	if cfg.ReaperPort < 1 || cfg.ReaperPort > 65535 {
		return Studio{}, fmt.Errorf("invalid configuration: %s must be between 1 and 65535 (got %d)",
			KeyReaperPort, cfg.ReaperPort)
	}
	if cfg.SessionDir == "" {
		return Studio{}, fmt.Errorf("invalid configuration: %s must be set", KeySessionDir)
	}
	if cfg.Session == "" {
		cfg.Session = "default"
	}
	return cfg, nil
}
