package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/studio"
	"github.com/ETLopes/cli/internal/ui"
)

// The levels view exists for gain staging, which is the one job that cannot be
// done from the routing alone. A channel twenty decibels down sounds like a
// quiet player rather than a mis-set preamp, and the only way to tell them
// apart is to watch the meter while turning the knob.

func newStudioLevelsCmd(e *env) *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "levels",
		Short: i18n.T("cmd.levels.short"),
		Long: `Shows what each input is actually receiving.

Set a preamp so its instrument peaks near -12 dB while it is played at
performance volume. That leaves headroom for the loudest hit without burying
the channel, and it is set at the interface: the gain knobs are analogue and
out of reach from here.

The same meters appear above the strip in 'cli studio', where TRIM sits
directly under them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStudio(e)
			if err != nil {
				return err
			}
			if once || e.plain {
				return printLevels(cmd.Context(), s)
			}
			_, err = tea.NewProgram(newLevelsModel(cmd.Context(), s)).Run()
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "print a single reading and exit")
	return cmd
}

// printLevels writes one reading, for a script or a log.
func printLevels(ctx context.Context, s *studioEnv) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	meters, err := s.dawc.Meters(c, studio.InstrumentIDs())
	if err != nil {
		return err
	}
	for _, line := range levelLines(meters, 90) {
		ui.Println(line)
	}
	return nil
}

// levelLines renders the whole meter bridge, one instrument per line.
func levelLines(meters map[string]daw.Meter, width int) []string {
	instruments := studio.Instruments()
	nameWidth := 4
	for _, in := range instruments {
		if n := len(in.Name); n > nameWidth {
			nameWidth = n
		}
	}
	bar := max(10, min(32, width-nameWidth-30))

	var out []string
	for _, in := range instruments {
		m := meters[in.ID]
		out = append(out, fmt.Sprintf("  %s  %s  %s  %s",
			ui.Heading.Render(ui.Pad(in.Name, nameWidth)),
			meterBar(m, bar),
			ui.Muted.Render(ui.Pad(meterLabel(m)+" dB", 8)),
			ui.Muted.Render(i18n.T(meterAdvice(m)))))
	}
	return out
}

// levelsModel is the live view.
type levelsModel struct {
	ctx     context.Context
	studio  *studioEnv
	meters  map[string]daw.Meter
	err     string
	width   int
	quiting bool
}

func newLevelsModel(ctx context.Context, s *studioEnv) levelsModel {
	return levelsModel{ctx: ctx, studio: s, meters: map[string]daw.Meter{}, width: 90}
}

func (m levelsModel) Init() tea.Cmd { return m.read() }

func (m levelsModel) read() tea.Cmd {
	st, ctx := m.studio, m.ctx
	ids := studio.InstrumentIDs()
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		got, err := st.dawc.Meters(c, ids)
		return metersMsg{meters: got, err: err}
	}
}

func (m levelsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case metersMsg:
		if msg.err != nil {
			m.err = firstLine(msg.err.Error())
		} else {
			m.err = ""
			m.meters = msg.meters
		}
		return m, tea.Tick(meterInterval, func(time.Time) tea.Msg { return meterTickMsg{} })
	case meterTickMsg:
		return m, m.read()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quiting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m levelsModel) View() tea.View {
	if m.quiting {
		return tea.NewView("")
	}
	var b strings.Builder
	b.WriteString("\n  " + ui.Title.Render(i18n.T("levels.title")) + "\n\n")
	for _, line := range levelLines(m.meters, m.width) {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	if m.err != "" {
		b.WriteString("  " + ui.Err.Render(m.err) + "\n")
	}
	b.WriteString("  " + ui.Muted.Render(i18n.T("levels.keys")) + "\n")
	return tea.NewView(b.String())
}
