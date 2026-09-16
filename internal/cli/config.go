package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/ui"
)

func newConfigCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and initialize cli configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// With a terminal, editing settings is the point; without one,
			// listing the subcommands is all that is useful.
			if !e.interactive() {
				return cmd.Help()
			}
			return runConfigTUI(e)
		},
	}
	cmd.AddCommand(
		newConfigSetCmd(e),
		newConfigShowCmd(e),
		newConfigPathCmd(),
		newConfigInitCmd(e),
	)
	return cmd
}

// newConfigSetCmd changes one setting and writes it back.
//
// Editing YAML by hand is a fine way to change several things at once, but a
// poor way to change one: it means knowing the file exists, where it lives,
// and what the key is called.
func newConfigSetCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one setting",
		Long: `Writes a single setting to the config file, creating it if needed.

  cli config set lang pt
  cli config set studio.reaper_port 9080
  cli config set dtx.model htdemucs_ft

Run 'cli config show' to see what is in effect.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := strings.ToLower(strings.TrimSpace(args[0])), args[1]
			if err := validateSetting(key, value); err != nil {
				return err
			}

			path := filepath.Join(config.Dir(), config.FileName+".yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}

			// Read what is there and write it back with the one change, so
			// settings the user has already made survive.
			v := viper.New()
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err != nil && !os.IsNotExist(err) {
				var notFound viper.ConfigFileNotFoundError
				if !errors.As(err, &notFound) {
					return fmt.Errorf("reading %s: %w", path, err)
				}
			}
			v.Set(key, typedValue(value))
			if err := v.WriteConfigAs(path); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}

			ui.Println(ui.Success(fmt.Sprintf("%s = %s", key, value)))
			ui.Println(ui.Muted.Render("  " + path))
			if key == config.KeyLang {
				// Confirm in the language just chosen, which is the quickest
				// proof it took effect.
				if l, ok := i18n.Parse(value); ok {
					i18n.Use(l)
					ui.Println(ui.Muted.Render("  " + i18n.T("toolbox.tagline")))
				}
			}
			return nil
		},
	}
}

// validateSetting rejects a value the application would refuse later anyway,
// so the mistake is caught at the moment it is made rather than on next run.
func validateSetting(key, value string) error {
	switch key {
	case config.KeyLang:
		if _, ok := i18n.Parse(value); !ok {
			var names []string
			for _, l := range i18n.Supported() {
				names = append(names, string(l)+" ("+l.Name()+")")
			}
			return fmt.Errorf("unknown language %q; choose from: %s",
				value, strings.Join(names, ", "))
		}
	case config.KeyDevice:
		switch value {
		case "auto", "cpu", "mps", "cuda":
		default:
			return fmt.Errorf("unknown device %q; choose from: auto, cpu, mps, cuda", value)
		}
	case config.KeyModel:
		if !slices.Contains(config.KnownModels, value) {
			return fmt.Errorf("unknown model %q; choose from: %s",
				value, strings.Join(config.KnownModels, ", "))
		}
	case config.KeyReaperPort:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("port must be a number between 1 and 65535, not %q", value)
		}
	}
	return nil
}

// typedValue keeps numbers and booleans out of the file as quoted strings,
// which would then fail to parse as the type the setting expects.
func typedValue(value string) any {
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	if b, err := strconv.ParseBool(value); err == nil {
		return b
	}
	return value
}

func newConfigShowCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the settings currently in effect",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ui.Println(ui.Banner("configuration"))
			ui.Println()

			rows := [][2]string{
				{"output_dir", e.cfg.OutputDir},
				{"model", e.cfg.Model},
				{"device", e.cfg.Device},
				{"shifts", strconv.Itoa(e.cfg.Shifts)},
				{"jobs", jobsLabel(e.cfg.Jobs)},
				{"normalize", strconv.FormatBool(e.cfg.Normalize)},
				{"limit", strconv.FormatBool(e.cfg.Limit)},
				{"usb_path", orDash(e.cfg.USBPath)},
				{"cookies_from_browser", orDash(e.cfg.CookiesFromBrowser)},
			}
			width := 0
			for _, r := range rows {
				if len(r[0]) > width {
					width = len(r[0])
				}
			}
			for _, r := range rows {
				ui.Println("  " + ui.KeyValue(r[0], r[1], width))
			}

			ui.Println()
			if used := e.v.ConfigFileUsed(); used != "" {
				ui.Println(ui.Muted.Render("  loaded from " + used))
			} else {
				ui.Println(ui.Muted.Render("  no config file; using defaults (run 'cli config init' to create one)"))
			}
			return nil
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print where cli looks for its config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(os.Stdout, filepath.Join(config.Dir(), config.FileName+".yaml"))
			return nil
		},
	}
}

func newConfigInitCmd(e *env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a config file preloaded with the current settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := filepath.Join(config.Dir(), config.FileName+".yaml")
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("creating config directory: %w", err)
			}

			// Settings are nested under the tool's section so other tools
			// can add their own without colliding. The studio's rig is
			// written out in full, so changing which instrument is on which
			// input is editing a file rather than rebuilding.
			type studioFile struct {
				config.Studio       `yaml:",inline"`
				config.TopologyFile `yaml:",inline"`
			}
			body, err := yaml.Marshal(map[string]any{
				config.KeyLang:       string(i18n.Current()),
				config.Section:       e.cfg,
				config.StudioSection: studioFile{e.studio, config.DefaultTopologyFile()},
			})
			if err != nil {
				return fmt.Errorf("encoding config: %w", err)
			}
			content := append([]byte(configHeader), body...)
			if err := os.WriteFile(path, content, 0o644); err != nil {
				return fmt.Errorf("writing config: %w", err)
			}

			ui.Println(ui.Success("wrote " + path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

const configHeader = `# cli configuration.
#
# Each tool owns a section. Every setting can be overridden by a command-line
# flag, or by an environment variable such as CLI_DTX_MODEL.
`

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func jobsLabel(n int) string {
	if n == 0 {
		return "auto"
	}
	return strconv.Itoa(n)
}
