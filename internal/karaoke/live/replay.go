package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ETLopes/cli/internal/karaoke/audioio"
	"github.com/ETLopes/cli/internal/karaoke/calibrate"
	"github.com/ETLopes/cli/internal/karaoke/lyrics"
	"github.com/ETLopes/cli/internal/karaoke/score"
	"github.com/ETLopes/cli/internal/karaoke/song"
)

// ReadBundle reads the manifest of a recorded session.
func ReadBundle(dir string) (Bundle, error) {
	data, err := os.ReadFile(filepath.Join(dir, bundleManifest))
	if err != nil {
		return Bundle{}, fmt.Errorf("read the recording: %w", err)
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("parse %s: %w", bundleManifest, err)
	}
	if b.Version != bundleVersion {
		return Bundle{}, fmt.Errorf("recording %s is version %d, this build reads version %d", dir, b.Version, bundleVersion)
	}
	return b, nil
}

// Replay runs a recorded session through exactly the processing a live one
// runs (the same pipeline.feed, with the recorded pause timeline and overflow
// gaps), offline and as fast as the CPU allows. cal is the calibration to use,
// normally the one the session ran with but possibly a new one, and sg the
// song (its reference and lyrics; the recording holds the audio actually
// played, not the song). It returns the scores and the per-mic metrics that
// tuning the canceller on a real room needs.
func Replay(ctx context.Context, bundleDir string, cal calibrate.Result, sg song.Song) (Results, Metrics, error) {
	b, err := ReadBundle(bundleDir)
	if err != nil {
		return Results{}, Metrics{}, err
	}
	if cal.SampleRate != b.SampleRate {
		return Results{}, Metrics{}, fmt.Errorf("the calibration is for %d Hz but the recording is %d Hz", cal.SampleRate, b.SampleRate)
	}
	ref, err := readMono(filepath.Join(bundleDir, bundleReference), b)
	if err != nil {
		return Results{}, Metrics{}, err
	}
	mics := make([][]float32, len(b.Inputs))
	for i, ch := range b.Inputs {
		if mics[i], err = readMono(filepath.Join(bundleDir, captureFile(ch)), b); err != nil {
			return Results{}, Metrics{}, err
		}
	}
	diff, err := score.ParseDifficulty(b.Difficulty)
	if err != nil {
		return Results{}, Metrics{}, err
	}
	grid, err := loadGrid(sg)
	if err != nil {
		return Results{}, Metrics{}, err
	}
	var lines []lyrics.Line
	if l, ok, err := sg.LoadLyrics(); err != nil {
		return Results{}, Metrics{}, err
	} else if ok {
		lines = l.Lines
	}
	specs := make([]laneSpec, len(b.Inputs))
	for i, ch := range b.Inputs {
		specs[i] = laneSpec{name: b.Players[i], channel: ch}
	}
	p, err := newPipeline(b.SampleRate, cal, specs, songData{grid: grid, lines: lines},
		score.Config{Difficulty: diff, Slack: time.Duration(b.SlackMS) * time.Millisecond}, timelineOf(b.Segments))
	if err != nil {
		return Results{}, Metrics{}, err
	}

	views := make([][]float32, len(mics))
	gi := 0
	for pos := int64(0); ; {
		for gi < len(b.Gaps) && b.Gaps[gi].At == pos {
			p.skip(b.Gaps[gi].Frames)
			gi++
		}
		if pos >= b.Frames {
			break
		}
		if err := ctx.Err(); err != nil {
			return Results{}, Metrics{}, err
		}
		end := min(pos+chunkFrames, b.Frames)
		if gi < len(b.Gaps) {
			end = min(end, b.Gaps[gi].At)
		}
		for i := range mics {
			views[i] = mics[i][pos:end]
		}
		p.feed(ref[pos:end], views)
		pos = end
	}

	res := p.finish(b.Incomplete)
	out := Results{Incomplete: b.Incomplete}
	for i, l := range p.lanes {
		out.Players = append(out.Players, PlayerResult{Name: l.name, Channel: l.channel, Result: res[i]})
	}
	return out, p.metrics(), nil
}

// readMono loads a bundle WAV and checks it against the manifest.
func readMono(path string, b Bundle) ([]float32, error) {
	a, err := audioio.ReadWAVFile(path)
	if err != nil {
		return nil, err
	}
	if a.Channels != 1 || a.SampleRate != b.SampleRate || int64(a.Frames()) != b.Frames {
		return nil, fmt.Errorf("%s is %d channel(s) at %d Hz with %d frames, the manifest says mono at %d Hz with %d",
			filepath.Base(path), a.Channels, a.SampleRate, a.Frames(), b.SampleRate, b.Frames)
	}
	return a.Samples, nil
}
