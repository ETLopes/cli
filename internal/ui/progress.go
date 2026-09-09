package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/eduardolopes/dtx/internal/pipeline"
)

// RunFunc performs the work being reported on, publishing progress through the
// observer it is handed.
type RunFunc func(pipeline.Observer) (*pipeline.Result, error)

// eventBuffer is deliberately generous: ffmpeg and Demucs emit progress far
// faster than a terminal needs redrawing, and a full buffer drops interim
// updates rather than stalling the process producing them.
const eventBuffer = 256

// stageState tracks how far a single pipeline stage has got.
type stageState struct {
	fraction float64
	detail   string
	done     bool
	started  bool
}

// RunPipeline executes fn while drawing a live progress view, returning the
// pipeline's own result. Pressing ctrl+c cancels via the supplied cancel func.
func RunPipeline(ctx context.Context, cancel context.CancelFunc, header string, fn RunFunc) (*pipeline.Result, error) {
	events := make(chan pipeline.Event, eventBuffer)
	type outcome struct {
		result *pipeline.Result
		err    error
	}
	done := make(chan outcome, 1)

	observe := func(e pipeline.Event) {
		select {
		case events <- e:
		default:
			// Buffer full: drop this update rather than stalling the pipeline.
			// Every stage sends a final Done event, so the view still settles
			// on the correct state.
		}
	}

	go func() {
		res, err := fn(observe)
		done <- outcome{res, err}
		close(events)
	}()

	// The program owns the terminal; it must not be torn down by the same
	// context that cancels the work, or the final frame would be lost.
	prog := tea.NewProgram(newProgressModel(header, cancel))

	go func() {
		for e := range events {
			prog.Send(eventMsg(e))
		}
	}()
	go func() {
		o := <-done
		prog.Send(finishedMsg{result: o.result, err: o.err})
	}()

	final, err := prog.Run()
	if err != nil {
		return nil, err
	}
	fm, ok := final.(progressModel)
	if !ok {
		return nil, fmt.Errorf("unexpected terminal model state")
	}
	if fm.interrupted {
		return nil, context.Canceled
	}
	if fm.err != nil {
		return nil, fm.err
	}
	return fm.result, nil
}

type eventMsg pipeline.Event

type finishedMsg struct {
	result *pipeline.Result
	err    error
}

type progressModel struct {
	header  string
	cancel  context.CancelFunc
	spinner spinner.Model
	bar     progress.Model

	order  []pipeline.Stage
	states map[pipeline.Stage]*stageState
	active pipeline.Stage

	warnings []string

	result      *pipeline.Result
	err         error
	finished    bool
	interrupted bool
}

func newProgressModel(header string, cancel context.CancelFunc) progressModel {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(Accent))

	bar := progress.New(progress.WithColors(BarColors...))
	// The percentage is rendered alongside the bar with consistent width, so
	// the component's own trailing label is redundant.
	bar.ShowPercentage = false
	bar.SetWidth(28)

	states := make(map[pipeline.Stage]*stageState, len(pipeline.Stages))
	for _, s := range pipeline.Stages {
		states[s] = &stageState{}
	}

	return progressModel{
		header:  header,
		cancel:  cancel,
		spinner: sp,
		bar:     bar,
		order:   pipeline.Stages,
		states:  states,
	}
}

func (m progressModel) Init() tea.Cmd { return m.spinner.Tick }

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.bar.SetWidth(clamp(msg.Width-46, 12, 36))
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.interrupted = true
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case eventMsg:
		e := pipeline.Event(msg)
		st, ok := m.states[e.Stage]
		if !ok {
			return m, nil
		}
		st.started = true
		if e.Detail != "" {
			st.detail = e.Detail
		}
		// Fractions only ever move forward: a late-arriving buffered event
		// must not make the bar jump backwards.
		if e.Fraction > st.fraction {
			st.fraction = e.Fraction
		}
		if e.Done {
			st.done = true
			st.fraction = 1
		}
		if e.Warning != "" {
			m.warnings = append(m.warnings, e.Warning)
		}
		m.active = e.Stage
		return m, nil

	case finishedMsg:
		m.result = msg.result
		m.err = msg.err
		m.finished = true
		return m, tea.Quit
	}
	return m, nil
}

func (m progressModel) View() tea.View {
	// Once finished, collapse the view entirely; the command prints its own
	// summary, and a stale progress block above it would just be noise.
	if m.finished || m.interrupted {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString(Banner(m.header))
	b.WriteString("\n\n")

	const labelWidth = 12
	for _, stage := range m.order {
		st := m.states[stage]
		label := Pad(string(stage), labelWidth)

		switch {
		case st.done:
			b.WriteString("  " + OK.Render(GlyphOK) + " " + label)
			if st.detail != "" {
				b.WriteString("  " + Muted.Render(truncate(st.detail, 44)))
			}
		case st.started && stage == m.active:
			b.WriteString("  " + m.spinner.View() + " " + label)
			b.WriteString("  " + m.bar.ViewAs(st.fraction))
			b.WriteString(fmt.Sprintf(" %3.0f%%", st.fraction*100))
			if st.detail != "" {
				b.WriteString("  " + Muted.Render(truncate(st.detail, 28)))
			}
		default:
			b.WriteString("  " + Muted.Render(GlyphPending+" "+label))
		}
		b.WriteString("\n")
	}

	for _, w := range m.warnings {
		b.WriteString("\n  " + Warning(w))
	}
	b.WriteString("\n" + Muted.Render("  ctrl+c to cancel") + "\n")
	return tea.NewView(b.String())
}

// PlainReporter writes progress as ordinary lines, for pipes, CI logs, and
// terminals that cannot render the live view.
type PlainReporter struct {
	w         io.Writer
	mu        sync.Mutex
	announced map[pipeline.Stage]bool
}

// NewPlainReporter returns a reporter writing to w.
func NewPlainReporter(w io.Writer) *PlainReporter {
	return &PlainReporter{w: w, announced: make(map[pipeline.Stage]bool)}
}

// Observe implements pipeline.Observer, emitting one line when a stage starts
// and one when it completes. Intermediate fractions are omitted, which keeps a
// piped log readable.
func (p *PlainReporter) Observe(e pipeline.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if e.Warning != "" {
		fmt.Fprintln(p.w, Warning(e.Warning))
	}
	switch {
	case e.Done:
		line := string(e.Stage)
		if e.Detail != "" {
			line += "  " + e.Detail
		}
		fmt.Fprintln(p.w, Success(line))
	case !p.announced[e.Stage]:
		p.announced[e.Stage] = true
		line := string(e.Stage) + "..."
		if e.Detail != "" {
			line += "  " + e.Detail
		}
		fmt.Fprintln(p.w, Muted.Render(GlyphBullet+" "+line))
	}
}

func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
