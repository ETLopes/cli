package live

import (
	"fmt"
	"math"
	"time"
)

// Snapshot returns the live view. It is cheap and safe to call from any
// goroutine while the session runs.
func (s *Session) Snapshot() Snapshot {
	st := s.stream.Stats()
	snap := Snapshot{
		Position:  s.frameTime(s.src.Position()),
		Duration:  s.frameTime(s.src.Len()),
		LineIndex: -1,
		Paused:    s.paused.Load(),
		Done:      s.fin.Load(),
		Stream: StreamStats{
			Frames: st.Frames.Load(), CaptureOverflows: st.CaptureOverflows.Load(),
			ReferenceOverflows: st.ReferenceOverflows.Load(), SourceUnderruns: st.SourceUnderruns.Load(),
		},
		Warnings: append([]string(nil), s.warnings...),
	}
	for i, l := range s.data.lines {
		if !l.Blank() && snap.Position >= l.Start && snap.Position < l.End {
			snap.LineIndex = i
			snap.LineProgress = float64(snap.Position-l.Start) / float64(l.End-l.Start)
			break
		}
	}
	for _, l := range s.pipe.lanes {
		ss := l.scorer.Snapshot()
		snap.Players = append(snap.Players, PlayerSnapshot{
			Name: l.name, Channel: l.channel, Score: ss.Score, Streak: ss.Streak, Last: ss.Last,
			SungMIDI: math.Float64frombits(l.sungMIDI.Load()), Voiced: l.isVoiced.Load(),
			TargetMIDI: ss.TargetMIDI, TargetVoiced: ss.TargetVoiced,
			LevelDBFS: math.Float64frombits(l.level.Load()),
		})
	}
	if n := st.CaptureOverflows.Load() + st.ReferenceOverflows.Load(); n > 0 {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("audio was dropped (%d samples): the machine is too busy", n))
	}
	if n := st.SourceUnderruns.Load(); n > 0 {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("playback underran (%d frames)", n))
	}
	if err := s.rec.failure(); err != nil {
		snap.Warnings = append(snap.Warnings, "recording stopped: "+err.Error())
	}
	return snap
}

func (s *Session) frameTime(frames int64) time.Duration {
	return time.Duration(frames) * time.Second / time.Duration(s.rate)
}
