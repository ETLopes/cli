package dsp

import (
	"math"
	"testing"
)

func tone(n int, f, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*f*float64(i)/cancRate))
	}
	return out
}

func newTestGate(t testing.TB, floor float64) *Gate {
	t.Helper()
	g, err := NewGate(GateConfig{ResidualFloor: floor})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGateSilenceGivesNoAcceptedFrames(t *testing.T) {
	g := newTestGate(t, 0)
	z := make([]float32, GateWindow)
	if d := g.Decide(z, z, z); d.Voiced || d.F0 != 0 {
		t.Errorf("silence accepted: %+v", d)
	}
	s := buildScenario(mainScenario(4)) // and through the whole chain, with nothing at all
	frames := streamChain(newTestChain(t, ChainConfig{ResidualFloor: 1e-4}), make([]float64, len(s.mic)), make([]float64, len(s.mic)))
	if got := rejectedShare(frames, 0); got != 1 {
		t.Errorf("%.1f%% of silent frames rejected, want 100%%", 100*got)
	}
}

func TestGateAcceptsAToneAboveTheFloorAndRejectsItBelow(t *testing.T) {
	ref := make([]float32, GateWindow)
	echo := make([]float32, GateWindow)
	if d := newTestGate(t, 0.01).Decide(tone(GateWindow, 330, 0.1), ref, echo); !d.Voiced || math.Abs(cents(d.F0, 330)) > 5 || d.Confidence < 0.8 {
		t.Errorf("a clear 330 Hz tone above the floor was not accepted: %+v", d)
	}
	// 0.1 amplitude is 0.07 RMS: a floor of 0.05 plus 6 dB is 0.1.
	if d := newTestGate(t, 0.05).Decide(tone(GateWindow, 330, 0.1), ref, echo); d.Voiced {
		t.Errorf("a tone below floor plus margin was accepted: %+v", d)
	}
}

func TestGateRejectsANoteTheReferenceExplainsUnlessTheSingerIsLoud(t *testing.T) {
	g := newTestGate(t, 1e-4)
	music := tone(GateWindow, 220, 0.3) // the backing holds a 220 Hz note
	echo := tone(GateWindow, 220, 0.3)
	silentRef := make([]float32, GateWindow)

	weak := tone(GateWindow, 220, 0.003) // residue of that note, 40 dB down
	if d := g.Decide(weak, music, echo); d.Voiced {
		t.Errorf("echo residue at the reference's own pitch accepted: %+v", d)
	}
	if d := g.Decide(weak, silentRef, echo); !d.Voiced {
		t.Errorf("the same signal was rejected with no partial in the reference: %+v", d)
	}
	loud := tone(GateWindow, 220, 0.2) // a singer on the same note
	if d := g.Decide(loud, music, echo); !d.Voiced {
		t.Errorf("a singer on the reference's note was rejected: %+v", d)
	}
	// An octave error counts too: YIN may lock onto 110 Hz or 440 Hz.
	if d := g.Decide(tone(GateWindow, 440, 0.003), music, echo); d.Voiced {
		t.Errorf("residue an octave above the reference's note accepted: %+v", d)
	}
	if d := g.Decide(tone(GateWindow, 110, 0.003), music, echo); d.Voiced {
		t.Errorf("residue an octave below the reference's note accepted: %+v", d)
	}
	// A pitch 100 cents away is not explained by that partial.
	if d := g.Decide(tone(GateWindow, 233.1, 0.003), music, echo); !d.Voiced {
		t.Errorf("residue a semitone from the reference's note rejected: %+v", d)
	}
}

// A sustained note in the backing and nobody singing: whatever the canceller
// leaves at that pitch must not be scored.
func TestGateRejectsFramesAtASustainedReferenceNoteWithNoSinger(t *testing.T) {
	const n = 8 * cancRate
	ref := make([]float64, n)
	for i, v := range harmonicVoice(n, constF0(220), []float64{1, 0.5, 0.3}, false) {
		ref[i] = 0.2 * v
	}
	r := newRoom(roomConfig{bulkDelay: cancBulk, rt60: 0.3, seed: chainRoom})
	echo := r.apply(softClip(ref, 1.5))
	mic := sumSignals(echo, whiteNoise(n, 1e-4, 9))
	// A deliberately wrong seed (the room measured a little off) leaves a
	// residual on the note itself, which the level test alone would pass.
	c := newTestChain(t, ChainConfig{ImpulseResponse: f32(r.ir[:len(r.ir)/2]), ResidualFloor: 1e-6})
	frames := streamChain(c, ref, mic)
	got := rejectedShare(frames, chainSettle)
	var loud int
	for _, f := range frames {
		if f.Level > 1e-6 {
			loud++
		}
	}
	t.Logf("sustained 220 Hz reference note, no singer: %.1f%% of %d frames rejected (%d above the floor)", 100*got, len(frames), loud)
	if got < 0.95 {
		t.Errorf("%.1f%% of frames at the reference's pitch rejected, want at least 95%%", 100*got)
	}
}

func TestGateDoesNotAllocateInSteadyState(t *testing.T) {
	g := newTestGate(t, 1e-4)
	out, ref, echo := tone(GateWindow, 330, 0.1), tone(GateWindow, 220, 0.3), tone(GateWindow, 220, 0.3)
	g.Decide(out, ref, echo)
	if n := testing.AllocsPerRun(50, func() { g.Decide(out, ref, echo) }); n != 0 {
		t.Errorf("Decide allocates %.0f times per frame, want 0", n)
	}
	weak := tone(GateWindow, 220, 0.003) // takes the reference-partial path too
	if n := testing.AllocsPerRun(50, func() { g.Decide(weak, ref, echo) }); n != 0 {
		t.Errorf("Decide allocates %.0f times per frame on the reference check, want 0", n)
	}
}
