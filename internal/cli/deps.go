package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/huh/v2"

	"github.com/ETLopes/cli/internal/deps"
	"github.com/ETLopes/cli/internal/runner"
	"github.com/ETLopes/cli/internal/separate"
	"github.com/ETLopes/cli/internal/ui"
)

// toolset is the set of resolved external tools a run will use.
type toolset struct {
	report     deps.Report
	demucsPath string
}

// ensureTools verifies every dependency is present, offering to install the one
// this program can manage. It returns an error only when the run genuinely
// cannot proceed.
func ensureTools(ctx context.Context, e *env) (*toolset, error) {
	r := runner.New()
	checker := deps.NewChecker(r)
	report := checker.Check(ctx)

	missing := report.Missing()
	if len(missing) == 0 {
		st, _ := report.Lookup(separate.Tool)
		return &toolset{report: report, demucsPath: st.Path}, nil
	}

	// Anything not managed by this program has to be installed by the user.
	var unmanaged []deps.Status
	needsDemucs := false
	for _, m := range missing {
		if m.Managed {
			needsDemucs = true
			continue
		}
		unmanaged = append(unmanaged, m)
	}
	if len(unmanaged) > 0 {
		var b strings.Builder
		b.WriteString("missing required tools:\n")
		for _, m := range unmanaged {
			fmt.Fprintf(&b, "\n  %s %s\n      install with: %s", ui.Err.Render(ui.GlyphFail), m.Name, m.Hint)
		}
		return nil, fmt.Errorf("%s", b.String())
	}

	if !needsDemucs {
		st, _ := report.Lookup(separate.Tool)
		return &toolset{report: report, demucsPath: st.Path}, nil
	}

	path, err := installDemucs(ctx, e, checker)
	if err != nil {
		return nil, err
	}
	return &toolset{report: checker.Check(ctx), demucsPath: path}, nil
}

// installDemucs provisions the managed Demucs environment, confirming first
// because it downloads on the order of a gigabyte.
func installDemucs(ctx context.Context, e *env, checker *deps.Checker) (string, error) {
	if e.interactive() {
		var confirmed bool
		err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Demucs is not installed.").
				Description("dtx can set it up in its own Python environment at\n" + deps.VenvDir() +
					"\n\nThis downloads PyTorch and is roughly 2 GB.").
				Affirmative("Install it").
				Negative("Cancel").
				Value(&confirmed),
		)).RunWithContext(ctx)
		if err != nil {
			return "", err
		}
		if !confirmed {
			return "", fmt.Errorf("demucs is required to separate stems; install it yourself, or re-run and accept the prompt")
		}
	} else if !e.assumeYes {
		return "", fmt.Errorf("demucs is not installed; re-run interactively, pass --yes to install it automatically, or install it yourself")
	}

	ui.Println(ui.Muted.Render(ui.GlyphBullet + " Installing demucs; this takes a few minutes."))

	onLine := func(_ runner.Stream, line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		slog.Debug("installer", "line", line)
		// uv reports progress in a compact form worth surfacing verbatim,
		// but only the meaningful milestones.
		if strings.HasPrefix(line, "Resolved") || strings.HasPrefix(line, "Installed") ||
			strings.HasPrefix(line, "Prepared") || strings.HasPrefix(line, "Using") {
			ui.Println(ui.Muted.Render("    " + line))
		}
	}

	path, err := checker.InstallDemucs(ctx, onLine)
	if err != nil {
		return "", err
	}
	ui.Println(ui.Success("demucs installed"))
	return path, nil
}
