package cli

import (
	"testing"

	"github.com/ETLopes/cli/internal/deps"
)

func TestOnlyAMissingSwiftF0IsInstalledOnItsOwn(t *testing.T) {
	swift := deps.Status{Name: deps.SwiftF0Package, Managed: true}
	demucs := deps.Status{Name: "demucs", Managed: true}
	deno := deps.Status{Name: "deno"}
	cases := []struct {
		name      string
		missing   []deps.Status
		envExists bool
		want      bool
	}{
		{"swift-f0 alone, with an environment", []deps.Status{swift}, true, true},
		{"swift-f0 alone, no environment yet", []deps.Status{swift}, false, false},
		{"demucs missing too", []deps.Status{demucs, swift}, true, false},
		{"only demucs missing", []deps.Status{demucs}, true, false},
		{"an unmanaged tool beside swift-f0 does not count", []deps.Status{deno, swift}, true, true},
		{"nothing managed missing", []deps.Status{deno}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlySwiftF0Missing(tc.missing, tc.envExists); got != tc.want {
				t.Errorf("onlySwiftF0Missing = %v, want %v", got, tc.want)
			}
		})
	}
}
