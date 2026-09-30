package audioio

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func TestWAVRoundTripsFloat32Stereo(t *testing.T) {
	in := &Audio{SampleRate: 48000, Channels: 2, Samples: testTrack(100)}
	var buf bytes.Buffer
	if err := WriteWAV(&buf, in, FormatFloat32); err != nil {
		t.Fatal(err)
	}
	out, err := ReadWAV(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if out.SampleRate != 48000 || out.Channels != 2 || len(out.Samples) != 200 {
		t.Fatalf("got rate %d channels %d samples %d", out.SampleRate, out.Channels, len(out.Samples))
	}
	for i := range in.Samples {
		if out.Samples[i] != in.Samples[i] {
			t.Fatalf("sample %d = %v, want %v", i, out.Samples[i], in.Samples[i])
		}
	}
}

func TestWAVRoundTripsInt16Mono(t *testing.T) {
	in := &Audio{SampleRate: 16000, Channels: 1, Samples: []float32{0, 0.5, -0.5, 1.5, -1.5}}
	var buf bytes.Buffer
	if err := WriteWAV(&buf, in, FormatInt16); err != nil {
		t.Fatal(err)
	}
	out, err := ReadWAV(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if out.SampleRate != 16000 || out.Channels != 1 {
		t.Fatalf("got rate %d channels %d", out.SampleRate, out.Channels)
	}
	want := []float32{0, 0.5, -0.5, 32767.0 / 32768, -1} // out-of-range values clip
	for i, w := range want {
		if math.Abs(float64(out.Samples[i]-w)) > 1e-4 {
			t.Fatalf("sample %d = %v, want %v", i, out.Samples[i], w)
		}
	}
}

// chunk builds a RIFF chunk with odd-size padding.
func chunk(id string, body []byte) []byte {
	b := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func fmtBody(tag uint16, ch, rate, bits int, extensible bool) []byte {
	size := 16
	if extensible {
		size = 40
	}
	b := make([]byte, size)
	binary.LittleEndian.PutUint16(b[0:], tag)
	binary.LittleEndian.PutUint16(b[2:], uint16(ch))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*ch*bits/8))
	binary.LittleEndian.PutUint16(b[12:], uint16(ch*bits/8))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	if extensible {
		binary.LittleEndian.PutUint16(b[16:], 22)
		binary.LittleEndian.PutUint16(b[18:], uint16(bits))
		binary.LittleEndian.PutUint16(b[24:], 3) // sub-format GUID starts with the real tag
	}
	return b
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	return append(b, body...)
}

func f32bytes(v ...float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func TestWAVReadsExtensibleFormatWithListFactAndOddPaddedChunks(t *testing.T) {
	file := riff(
		chunk("LIST", []byte("INFOISFT\x05\x00\x00\x00Lavf\x00")),
		chunk("fmt ", fmtBody(0xFFFE, 2, 48000, 32, true)),
		chunk("fact", []byte{2, 0, 0, 0}),
		chunk("odd ", []byte{1, 2, 3}), // odd size, padded
		chunk("data", f32bytes(0.25, -0.25, 0.5, -0.5)),
	)
	a, err := ReadWAV(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{0.25, -0.25, 0.5, -0.5}
	if a.SampleRate != 48000 || a.Channels != 2 || len(a.Samples) != 4 {
		t.Fatalf("got %+v", a)
	}
	for i, w := range want {
		if a.Samples[i] != w {
			t.Fatalf("sample %d = %v, want %v", i, a.Samples[i], w)
		}
	}
}

func TestWAVReadsFloatDataWithAStreamingUnknownLength(t *testing.T) {
	data := chunk("data", f32bytes(1, -1))
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFFFF)
	a, err := ReadWAV(bytes.NewReader(riff(chunk("fmt ", fmtBody(3, 1, 48000, 32, false)), data)))
	if err != nil || len(a.Samples) != 2 {
		t.Fatalf("err %v audio %+v", err, a)
	}
}

func TestWAVReportsClearErrorsForBadFiles(t *testing.T) {
	good := riff(chunk("fmt ", fmtBody(3, 2, 48000, 32, false)), chunk("data", f32bytes(1, 2, 3, 4)))
	cases := map[string]struct {
		data []byte
		want string
	}{
		"truncated data":   {good[:len(good)-5], "truncated"},
		"truncated header": {good[:6], "truncated"},
		"not riff":         {append([]byte("JUNK"), good[4:]...), "not a RIFF"},
		"no data":          {riff(chunk("fmt ", fmtBody(3, 2, 48000, 32, false))), "no data chunk"},
		"no fmt":           {riff(chunk("data", f32bytes(1))), "before the fmt"},
		"24-bit":           {riff(chunk("fmt ", fmtBody(1, 1, 48000, 24, false)), chunk("data", []byte{0, 0, 0})), "unsupported"},
	}
	for name, c := range cases {
		_, err := ReadWAV(bytes.NewReader(c.data))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}
