package score

import (
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/reference"
)

func TestPushAllocatesNothingInSteadyState(t *testing.T) {
	grid := reference.Grid{Step: step, MIDI: make([]float64, 6000), Voiced: make([]bool, 6000)}
	for i := range grid.MIDI {
		grid.MIDI[i], grid.Voiced[i] = 60, true
	}
	s := New(grid, nil, Config{})
	i := 0
	push := func() {
		s.Push(Frame{T: time.Duration(i) * step, MIDI: 60, Voiced: true})
		i++
	}
	for range 200 {
		push()
	}
	if got := testing.AllocsPerRun(1000, push); got != 0 {
		t.Errorf("Push allocated %v times per call in steady state, want 0", got)
	}
}
