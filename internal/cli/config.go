package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/ui"
)

func newConfigCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and initialize cli configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newConfigShowCmd(e),
		newConfigPathCmd(),
		newConfigInitCmd(e),
	)
	return cmd
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
		Short: "Print where dtx looks for its config file",
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
			// can add their own without colliding.
			body, err := yaml.Marshal(map[string]any{config.Section: e.cfg})
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
