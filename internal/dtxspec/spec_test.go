package dtxspec

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"spaces become camel case", "never gonna give you up", "NeverGonnaGiveYouUp"},
		{"punctuation is dropped", "Hello, World! (Live)", "HelloWorldLive"},
		{"accents fold to ascii", "Ação de Graças", "AcaoDeGracas"},
		{"cedilla folds", "Garoto de Aluguel — Coração", "GarotoDeAluguelCoracao"},
		{"digits are kept", "Blink 182 - 1st Song", "Blink1821stSong"},
		{"leading digit survives", "99 Problems", "99Problems"},
		{"already clean is unchanged", "CleanName", "CleanName"},
		{"empty falls back", "", "Track"},
		{"only punctuation falls back", "!!! ??? ---", "Track"},
		{"non-latin script falls back", "日本語", "Track"},
		{"underscores are boundaries", "my_track_name", "MyTrackName"},
		{"apostrophe stays inside the word", "What's Up", "WhatsUp"},
		{"curly apostrophe too", "What\u2019s Up", "WhatsUp"},
		{"possessive", "Rock'n'Roll", "RocknRoll"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeName(tt.in); got != tt.want {
				t.Errorf("SanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The module only renders alphanumeric characters, so a sanitized name that
// still contains anything else would display as garbage on the device.
func TestSanitizeNameIsAlwaysAlphanumeric(t *testing.T) {
	inputs := []string{
		"Ação de Graças", "!!! ???", "日本語 mixed with ASCII",
		"a/b\\c:d*e?f\"g<h>i|j", "tab\there", "emoji 🎵 drums",
		strings.Repeat("very long title ", 20),
	}
	for _, in := range inputs {
		got := SanitizeName(in)
		if got == "" {
			t.Errorf("SanitizeName(%q) returned empty", in)
		}
		for _, r := range got {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !isAlnum {
				t.Errorf("SanitizeName(%q) = %q contains non-alphanumeric %q", in, got, r)
			}
		}
		if len([]rune(got)) > MaxNameLength {
			t.Errorf("SanitizeName(%q) = %q is %d runes, over the %d limit",
				in, got, len([]rune(got)), MaxNameLength)
		}
	}
}

func TestNamer(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		variants []string
		variant  string
		want     string
	}{
		{"simple", "MySong", []string{"Full", "NoDrums"}, "NoDrums", "MySongNoDrums.wav"},
		{"full mix", "MySong", []string{"Full"}, "Full", "MySongFull.wav"},
		{"variant sanitized", "MySong", []string{"No Drums!"}, "No Drums!", "MySongNoDrums.wav"},
		{"empty variant leaves the base alone", "MySong", []string{""}, "", "MySong.wav"},
		{"empty title still names the variant", "", []string{"NoDrums"}, "NoDrums", "TrackNoDrums.wav"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewNamer(tt.title, tt.variants).Name(tt.variant)
			if got != tt.want {
				t.Errorf("Name(%q) = %q, want %q", tt.variant, got, tt.want)
			}
		})
	}
}

// Every file in a set must share one base, so they align on the module's
// display and sort together. Shrinking each name to fit its own suffix would
// produce a different base per file.
func TestNamerUsesOneBaseForTheWholeSet(t *testing.T) {
	variants := []string{"Full", "NoDrums", "NoBass", "NoVocals", "NoOther", "Drums", "Bass", "Vocals", "Other"}
	namer := NewNamer("An Extremely Long Song Title That Goes On Forever", variants)

	var base string
	seen := make(map[string]string, len(variants))
	for _, v := range variants {
		got := namer.Name(v)
		stem := strings.TrimSuffix(got, ".wav")

		if !strings.HasSuffix(stem, v) {
			t.Errorf("Name(%q) = %q, expected it to end with %q", v, got, v)
			continue
		}
		prefix := strings.TrimSuffix(stem, v)
		if base == "" {
			base = prefix
		} else if prefix != base {
			t.Errorf("Name(%q) has base %q, but the set uses %q", v, prefix, base)
		}
		if len([]rune(stem)) > MaxNameLength {
			t.Errorf("Name(%q) = %q is %d runes, over the %d limit", v, got, len([]rune(stem)), MaxNameLength)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("duplicate name %q for both %q and %q", got, prev, v)
		}
		seen[got] = v
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"official music video", "4 Non Blondes - What's Up (Official Music Video)", "4 Non Blondes - What's Up"},
		{"bracketed official video", "Song Name [Official Video]", "Song Name"},
		{"lyric video", "Song Name (Lyrics)", "Song Name"},
		{"trailing without brackets", "Song Name - Official Video", "Song Name"},
		{"resolution noise", "Song Name (HD)", "Song Name"},
		{"remastered", "Song Name (2011 Remastered)", "Song Name"},
		// These change how a drummer plays the track, so they must survive.
		{"live is kept", "Song Name (Live)", "Song Name (Live)"},
		{"acoustic is kept", "Song Name (Acoustic)", "Song Name (Acoustic)"},
		{"remix is kept", "Song Name (Radio Remix)", "Song Name (Radio Remix)"},
		{"plain title untouched", "Just A Song", "Just A Song"},
		// Cleaning must never leave nothing behind.
		{"all noise falls back", "(Official Music Video)", "(Official Music Video)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanTitle(tt.in); got != tt.want {
				t.Errorf("CleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Stripping boilerplate is what lets the actual song name survive truncation.
func TestNoiseStrippingPreservesTheSongName(t *testing.T) {
	variants := []string{"Full", "NoDrums", "NoVocals"}
	got := NewNamer("4 Non Blondes - What's Up (Official Music Video)", variants).Name("NoDrums")
	if got != "4NonBlondesWhatsUpNoDrums.wav" && !strings.HasPrefix(got, "4NonBlondesWhatsUp") {
		t.Errorf("Name = %q, want the song name to survive rather than the boilerplate", got)
	}
	if strings.Contains(got, "Official") {
		t.Errorf("Name = %q, should not contain production boilerplate", got)
	}
}

func TestFormatDescription(t *testing.T) {
	got := FormatDescription()
	// Integer division would render this as "44 kHz", which is simply wrong.
	if !strings.Contains(got, "44.1 kHz") {
		t.Errorf("FormatDescription() = %q, want it to say 44.1 kHz", got)
	}
	if !strings.Contains(got, "16-bit") {
		t.Errorf("FormatDescription() = %q, want it to mention 16-bit", got)
	}
}

func TestFormatConforms(t *testing.T) {
	good := Format{Codec: Codec, SampleRate: SampleRate, Channels: Channels}
	if !good.Conforms() {
		t.Error("a 44.1kHz 16-bit stereo PCM file should conform")
	}
	if d := good.Deviations(); d != nil {
		t.Errorf("conforming format reported deviations: %v", d)
	}

	tests := []struct {
		name string
		f    Format
		want string
	}{
		{"wrong rate", Format{Codec: Codec, SampleRate: 48000, Channels: 2}, "48000 Hz"},
		{"mono", Format{Codec: Codec, SampleRate: SampleRate, Channels: 1}, "1 channel(s)"},
		{"compressed", Format{Codec: "aac", SampleRate: SampleRate, Channels: 2}, "codec aac"},
		{"unknown codec reads clearly", Format{SampleRate: SampleRate, Channels: 2}, "codec unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.f.Conforms() {
				t.Fatalf("%+v should not conform", tt.f)
			}
			devs := strings.Join(tt.f.Deviations(), "; ")
			if !strings.Contains(devs, tt.want) {
				t.Errorf("Deviations() = %q, want it to mention %q", devs, tt.want)
			}
		})
	}
}

func TestExceedsDuration(t *testing.T) {
	if ExceedsDuration(89 * time.Minute) {
		t.Error("89 minutes is within the module's limit")
	}
	if !ExceedsDuration(91 * time.Minute) {
		t.Error("91 minutes exceeds the module's 90 minute limit")
	}
}

func TestBytesPerSecond(t *testing.T) {
	// 44100 samples * 2 channels * 2 bytes = 176400 bytes per second.
	if BytesPerSecond != 176400 {
		t.Errorf("BytesPerSecond = %d, want 176400", BytesPerSecond)
	}
}
