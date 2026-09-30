package live

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// Bundle file names.
const (
	bundleManifest  = "session.json"
	bundleReference = "reference.wav"
	bundleVersion   = 1
)

func captureFile(channel int) string { return fmt.Sprintf("input-%d.wav", channel) }

// gap is a stretch of stream a ring overflow lost, at consumed frame At.
type gap struct {
	At     int64 `json:"at"`
	Frames int64 `json:"frames"`
}

// CalibrationKey identifies the calibration a session ran with. The bundle
// does not carry the calibration itself: a replay is given one, so a new
// calibration (or a tweak to the canceller) can be tried on old recordings.
type CalibrationKey struct {
	Device     string `json:"device"`
	SampleRate int    `json:"sample_rate"`
	Outputs    [2]int `json:"outputs"`
	Inputs     []int  `json:"inputs"`
}

// Bundle is session.json, the manifest of a recorded session. Beside it lie
// reference.wav, the mono device-rate reference as played, and input-<n>.wav,
// the raw capture of each input at the device rate, all float32 and all as
// long as the stream ran (Frames), sample-aligned.
type Bundle struct {
	Version     int            `json:"version"`
	SongDir     string         `json:"song_dir"`
	Calibration CalibrationKey `json:"calibration"`
	SampleRate  int            `json:"sample_rate"`
	// Inputs are the capture channels and Players their names, in the order
	// the session used.
	Inputs     []int    `json:"inputs"`
	Players    []string `json:"players"`
	Difficulty string   `json:"difficulty"`
	SlackMS    int64    `json:"slack_ms"`
	// Segments are the pause/resume timeline, in stream frames.
	Segments []segment `json:"segments"`
	// Gaps are ring overflows: frames lost after a given recorded frame.
	Gaps       []gap `json:"gaps,omitempty"`
	Frames     int64 `json:"frames"`
	Incomplete bool  `json:"incomplete,omitempty"`
}

// recorder streams the raw audio of a session to disk. It is used from the
// processing goroutine only (Failure aside). Every method accepts a nil
// receiver, so a session without a record dir needs no branches.
type recorder struct {
	dir  string
	ref  *wavWriter
	caps []*wavWriter
	gaps []gap

	mu  sync.Mutex
	err error
}

func newRecorder(dir string, rate int, channels []int) (*recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &recorder{dir: dir}
	var err error
	if r.ref, err = newWAVWriter(filepath.Join(dir, bundleReference), rate); err != nil {
		return nil, err
	}
	for _, ch := range channels {
		w, err := newWAVWriter(filepath.Join(dir, captureFile(ch)), rate)
		if err != nil {
			r.abort(err)
			return nil, err
		}
		r.caps = append(r.caps, w)
	}
	return r, nil
}

// Failure is the error that stopped the recording, if any.
func (r *recorder) failure() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *recorder) write(ref []float32, mics [][]float32) {
	if r == nil || r.failure() != nil {
		return
	}
	if err := r.ref.write(ref); err != nil {
		r.abort(err)
		return
	}
	for i, w := range r.caps {
		if err := w.write(mics[i]); err != nil {
			r.abort(err)
			return
		}
	}
}

func (r *recorder) gap(at, n int64) {
	if r != nil {
		r.gaps = append(r.gaps, gap{At: at, Frames: n})
	}
}

// abort stops the recording after a disk error. The session goes on.
func (r *recorder) abort(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	r.ref.abandon()
	for _, w := range r.caps {
		w.abandon()
	}
}

// finish flushes the audio and writes session.json.
func (r *recorder) finish(b Bundle) {
	if r == nil || r.failure() != nil {
		return
	}
	b.Version, b.Gaps = bundleVersion, r.gaps
	for _, w := range append([]*wavWriter{r.ref}, r.caps...) {
		if err := w.close(); err != nil {
			r.abort(err)
			return
		}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.dir, bundleManifest), data, 0o644)
	}
	if err != nil {
		r.abort(err)
	}
}

// bundle describes the session for the recorder. Called with s.mu held.
func (s *Session) bundle(incomplete bool) Bundle {
	cal := s.cfg.Calibration
	key := CalibrationKey{Device: cal.Device, SampleRate: cal.SampleRate, Outputs: cal.Outputs}
	for _, in := range cal.Inputs {
		key.Inputs = append(key.Inputs, in.Channel)
	}
	slack := s.cfg.Slack.Milliseconds()
	return Bundle{
		SongDir: s.cfg.Song.Dir, Calibration: key, SampleRate: s.rate,
		Inputs: s.cfg.Stream.Inputs, Players: s.cfg.Players,
		Difficulty: s.cfg.Difficulty.String(), SlackMS: slack,
		Segments: s.tl.segments(), Frames: s.consumed, Incomplete: incomplete,
	}
}

// wavWriter streams float32 mono samples into a WAV file through a buffer and
// patches the sizes into the header when closed.
type wavWriter struct {
	f       *os.File
	w       *bufio.Writer
	rate    int
	samples int64
	buf     []byte
	dead    bool
}

func newWAVWriter(path string, rate int) (*wavWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &wavWriter{f: f, rate: rate, w: bufio.NewWriterSize(f, 1<<16), buf: make([]byte, 4*chunkFrames)}
	if _, err := w.w.Write(wavHeader(rate, 0)); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// wavHeader is the canonical 44-byte float32 mono header for n samples.
func wavHeader(rate int, n int64) []byte {
	h := make([]byte, 44)
	le := binary.LittleEndian
	dataLen := uint32(n * 4)
	copy(h[0:], "RIFF")
	le.PutUint32(h[4:], 36+dataLen)
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	le.PutUint16(h[20:], 3) // IEEE float
	le.PutUint16(h[22:], 1)
	le.PutUint32(h[24:], uint32(rate))
	le.PutUint32(h[28:], uint32(rate*4))
	le.PutUint16(h[32:], 4)
	le.PutUint16(h[34:], 32)
	copy(h[36:], "data")
	le.PutUint32(h[40:], dataLen)
	return h
}

func (w *wavWriter) write(x []float32) error {
	if w.dead {
		return nil
	}
	if need := 4 * len(x); need > len(w.buf) {
		w.buf = make([]byte, need)
	}
	for i, v := range x {
		binary.LittleEndian.PutUint32(w.buf[4*i:], math.Float32bits(v))
	}
	if _, err := w.w.Write(w.buf[:4*len(x)]); err != nil {
		return err
	}
	w.samples += int64(len(x))
	return nil
}

// close flushes and patches the sizes into the header.
func (w *wavWriter) close() error {
	if w.dead {
		return nil
	}
	w.dead = true
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	if _, err := w.f.WriteAt(wavHeader(w.rate, w.samples), 0); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// abandon closes the file without finishing it.
func (w *wavWriter) abandon() {
	if w != nil && !w.dead {
		w.dead = true
		w.f.Close()
	}
}
