# Extracts the sung pitch contour from a vocal WAV with SwiftF0.
#
# Usage: python -c "<this script>" <input.wav> <output.json>
#
# It reads the WAV with the standard library rather than SwiftF0's file helper,
# which may need the optional soundfile package. Only numpy and swift_f0 are
# imported, so it runs in the managed venv as installed.
import json
import sys
import wave

import numpy as np
from swift_f0 import SwiftF0

FMIN_HZ = 60
FMAX_HZ = 1100


def read_wav(path):
    with wave.open(path, "rb") as w:
        if w.getsampwidth() != 2:
            raise SystemExit("expected 16-bit PCM WAV, got %d-bit" % (8 * w.getsampwidth()))
        rate = w.getframerate()
        channels = w.getnchannels()
        raw = w.readframes(w.getnframes())
    audio = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
    if channels > 1:
        audio = audio.reshape(-1, channels).mean(axis=1)
    return audio, rate


def clean(values, decimals, neginf=0.0):
    arr = np.nan_to_num(np.asarray(values, dtype=np.float64), nan=0.0, posinf=0.0, neginf=neginf)
    return np.round(arr, decimals).tolist()


def main():
    wav_path, out_path = sys.argv[1], sys.argv[2]
    audio, rate = read_wav(wav_path)
    result = SwiftF0().detect(audio, rate, fmin=FMIN_HZ, fmax=FMAX_HZ)

    times = np.asarray(result.timestamps, dtype=np.float64)
    hop = float(np.median(np.diff(times))) if len(times) > 1 else 0.016
    payload = {
        "hop": round(hop, 6),
        "sample_rate": 16000,
        "time": clean(times, 4),
        "hz": clean(result.pitch_hz, 3),
        "confidence": clean(result.confidence, 3),
        "loudness_db": clean(result.loudness_db, 2, neginf=-120.0),
    }
    with open(out_path, "w") as f:
        json.dump(payload, f, allow_nan=False)


main()
