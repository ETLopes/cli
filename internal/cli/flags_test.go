package cli

import (
	"testing"

	"github.com/ETLopes/cli/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// newFlagTree builds root → <tool> → <sub>, each leaf with an --out flag, the
// shape in which dtx and karaoke both take one.
func newFlagTree(tool, sub string) (*cobra.Command, *cobra.Command) {
	root := &cobra.Command{Use: "cli"}
	parent := &cobra.Command{Use: tool}
	leaf := &cobra.Command{Use: sub}
	leaf.Flags().String("out", "", "")
	parent.AddCommand(leaf)
	root.AddCommand(parent)
	return root, leaf
}

func TestDtxFlagsStillBindToTheirConfigKeys(t *testing.T) {
	_, leaf := newFlagTree("dtx", "prep")
	if err := leaf.Flags().Set("out", "/tmp/songs"); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	bindChangedFlags(leaf, v)
	if got := v.GetString(config.KeyOutputDir); got != "/tmp/songs" {
		t.Errorf("dtx --out bound %q to %s, want /tmp/songs", got, config.KeyOutputDir)
	}
}

func TestAnotherToolsFlagOfTheSameNameLeavesDtxSettingsAlone(t *testing.T) {
	_, leaf := newFlagTree("karaoke", "export")
	if err := leaf.Flags().Set("out", "/tmp/ultrastar"); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	bindChangedFlags(leaf, v)
	if v.IsSet(config.KeyOutputDir) {
		t.Errorf("karaoke export --out set %s to %q", config.KeyOutputDir, v.GetString(config.KeyOutputDir))
	}
}
