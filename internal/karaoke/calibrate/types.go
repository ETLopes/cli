package calibrate

import "time"

// Result is one calibration: everything measured for one device, rate and
// output pair, for each selected microphone.
type Result struct {
	Device     string    `json:"device"`
	SampleRate int       `json:"sample_rate"`
	Outputs    [2]int    `json:"outputs"`
	Time       time.Time `json:"time"`
	Inputs     []Input   `json:"inputs"`
}

// Input is the measurement of one microphone.
type Input struct {
	// Channel is the 1-based capture channel.
	Channel int `json:"channel"`
	// NoEchoPath means the impulse response had no clear direct-path peak above
	// the noise (unplugged, muted, gain at zero). Such an input is not seeded and
	// its other fields, apart from NoiseFloorDBFS, are unset.
	NoEchoPath bool `json:"no_echo_path,omitempty"`
	// DelaySamples and DelayMs are the round trip from playback to this mic, at
	// 16 kHz, measured by GCC-PHAT.
	DelaySamples float64 `json:"delay_samples"`
	DelayMs      float64 `json:"delay_ms"`
	// BulkDelay is the canceller's delay: DelaySamples rounded down, less a few
	// samples so the pre-ringing of the direct sound lies inside the filter.
	BulkDelay int `json:"bulk_delay"`
	// IR is the echo path at 16 kHz starting BulkDelay samples after the
	// reference was played and trimmed to the echo tail. See SeedIR.
	IR []float32 `json:"ir"`
	// TailMs is the echo tail after the direct sound, to -40 dB, clamped.
	TailMs float64 `json:"tail_ms"`
	// NoiseFloorDBFS is the mic level with nothing playing, at the device rate.
	NoiseFloorDBFS float64 `json:"noise_floor_dbfs"`
	// ResidualFloorDBFS is the RMS left after a canceller seeded from IR has
	// processed the noise segment (its settled part); ERLEdB is the reduction it
	// achieved over the same stretch.
	ResidualFloorDBFS float64 `json:"residual_floor_dbfs"`
	ERLEdB            float64 `json:"erle_db"`
	// Warnings are non-fatal observations, e.g. the two delay estimates disagree.
	Warnings []string `json:"warnings,omitempty"`
}
