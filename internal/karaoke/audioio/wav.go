package audioio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Audio is decoded PCM: interleaved float32 samples in [-1, 1].
type Audio struct {
	SampleRate int
	Channels   int
	Samples    []float32
}

// Frames is the number of sample frames.
func (a *Audio) Frames() int {
	if a.Channels == 0 {
		return 0
	}
	return len(a.Samples) / a.Channels
}

// WAVFormat selects the sample encoding WriteWAV produces.
type WAVFormat int

const (
	FormatFloat32 WAVFormat = iota // format tag 3
	FormatInt16                    // format tag 1
)

const (
	wavTagPCM        = 1
	wavTagFloat      = 3
	wavTagExtensible = 0xFFFE
)

// ReadWAVFile reads a WAV file from disk.
func ReadWAVFile(path string) (*Audio, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read wav: %w", err)
	}
	defer f.Close()
	a, err := ReadWAV(f)
	if err != nil {
		return nil, fmt.Errorf("read wav %s: %w", path, err)
	}
	return a, nil
}

// ReadWAV decodes float32 (format 3) and int16 (format 1) PCM, mono or stereo or
// wider, including WAVE_FORMAT_EXTENSIBLE. Unknown chunks (LIST, fact, ...) are
// skipped, honoring the pad byte after odd-sized chunks. ffmpeg writing to a pipe
// leaves the data length as 0xFFFFFFFF; that is read to the end of the input.
func ReadWAV(r io.Reader) (*Audio, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, errors.New("wav file truncated in the RIFF header")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var (
		haveFmt        bool
		tag, bits      int
		channels, rate int
		pos            = 12
		le             = binary.LittleEndian
	)
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(le.Uint32(data[pos+4:]))
		body := pos + 8
		if id == "data" && le.Uint32(data[pos+4:]) == 0xFFFFFFFF {
			size = len(data) - body
		}
		if body+size > len(data) {
			return nil, fmt.Errorf("wav file truncated in the %q chunk: need %d bytes, have %d", id, size, len(data)-body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("wav fmt chunk is too short")
			}
			b := data[body : body+size]
			tag = int(le.Uint16(b[0:]))
			channels = int(le.Uint16(b[2:]))
			rate = int(le.Uint32(b[4:]))
			bits = int(le.Uint16(b[14:]))
			if tag == wavTagExtensible {
				if size < 26 {
					return nil, errors.New("wav extensible fmt chunk is too short")
				}
				tag = int(le.Uint16(b[24:])) // first two bytes of the sub-format GUID
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, errors.New("wav data chunk comes before the fmt chunk")
			}
			return decodeWAV(data[body:body+size], tag, bits, channels, rate)
		}
		pos = body + size + size%2
	}
	return nil, errors.New("wav file has no data chunk")
}

func decodeWAV(b []byte, tag, bits, channels, rate int) (*Audio, error) {
	if channels < 1 {
		return nil, errors.New("wav file declares no channels")
	}
	a := &Audio{SampleRate: rate, Channels: channels}
	switch {
	case tag == wavTagFloat && bits == 32:
		n := len(b) / 4
		a.Samples = make([]float32, n)
		for i := range a.Samples {
			a.Samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
	case tag == wavTagPCM && bits == 16:
		n := len(b) / 2
		a.Samples = make([]float32, n)
		for i := range a.Samples {
			a.Samples[i] = float32(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
		}
	default:
		return nil, fmt.Errorf("unsupported wav encoding: format tag %d with %d bits (want float32 or int16)", tag, bits)
	}
	a.Samples = a.Samples[:len(a.Samples)/channels*channels]
	return a, nil
}

// WriteWAV encodes audio as a canonical 44-byte-header WAV. Int16 output clips
// samples outside [-1, 1).
func WriteWAV(w io.Writer, a *Audio, format WAVFormat) error {
	tag, bits := wavTagFloat, 32
	if format == FormatInt16 {
		tag, bits = wavTagPCM, 16
	}
	bytesPer := bits / 8
	dataLen := len(a.Samples) * bytesPer
	buf := make([]byte, 44+dataLen)
	le := binary.LittleEndian
	copy(buf[0:], "RIFF")
	le.PutUint32(buf[4:], uint32(36+dataLen))
	copy(buf[8:], "WAVEfmt ")
	le.PutUint32(buf[16:], 16)
	le.PutUint16(buf[20:], uint16(tag))
	le.PutUint16(buf[22:], uint16(a.Channels))
	le.PutUint32(buf[24:], uint32(a.SampleRate))
	le.PutUint32(buf[28:], uint32(a.SampleRate*a.Channels*bytesPer))
	le.PutUint16(buf[32:], uint16(a.Channels*bytesPer))
	le.PutUint16(buf[34:], uint16(bits))
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(dataLen))
	for i, s := range a.Samples {
		if format == FormatInt16 {
			v := math.Round(float64(s) * 32768)
			le.PutUint16(buf[44+2*i:], uint16(int16(min(max(v, -32768), 32767))))
		} else {
			le.PutUint32(buf[44+4*i:], math.Float32bits(s))
		}
	}
	_, err := w.Write(buf)
	return err
}
