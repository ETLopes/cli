# dtx

Turn any video into practice tracks a **Yamaha DTX-PRO** drum module will play.

Give it a URL and it downloads the audio, splits it into instrument stems with
[Demucs](https://github.com/adefossez/demucs), and renders everything as the
44.1 kHz / 16-bit / stereo WAV the module requires — including a **"minus-one"
mix for every instrument**, so you can mute the drums and play them yourself.

```
$ dtx https://www.youtube.com/watch?v=...

dtx  preparing practice tracks

  ✓ Inspecting    Some Song (Official Video)
  ✓ Downloading   source.m4a
  ⠸ Separating    ████████████░░░░░░░░  62%  htdemucs
    Rendering
    Exporting
```

## What you get

For a track called `Some Song`, with the default 4-stem model:

```
~/Music/dtx/some-song-<video-id>/
├── dtx/                        ← everything here is module-ready
│   ├── SomeSongFull.wav        ← the original mix
│   ├── SomeSongNoDrums.wav     ← play the drums yourself
│   ├── SomeSongNoBass.wav
│   ├── SomeSongNoVocals.wav
│   ├── SomeSongNoOther.wav
│   ├── SomeSongDrums.wav       ← isolated stems
│   ├── SomeSongBass.wav
│   ├── SomeSongVocals.wav
│   └── SomeSongOther.wav
├── flac/                       ← same nine tracks, other formats
├── opus/
├── stems/                      ← raw Demucs output (cached for re-runs)
└── source.m4a                  ← the original download
```

Copy the contents of `dtx/` to the **root** of a USB stick — the module does not
look inside folders — or let `dtx` do it with `--usb /Volumes/YOUR_DRIVE`. Only
the WAVs are copied to the drive; the module cannot play anything else.

## Sharing

WAV is what the module needs, but 443 MiB is awkward to send to a bandmate. Any
of these can be produced alongside it, each in its own folder so you can zip one
and send it:

| Format | Size (4:53 track, 9 files) | Notes |
|---|---|---|
| `wav` | 443 MiB | What the module plays. Always produced. |
| `flac` | ~245 MiB | Lossless — decodes bit-identical to the WAV. Safe to re-mix. |
| `opus` | ~40 MiB | Smallest. Transparent for listening; best quality per byte of any codec. |
| `m4a` | ~80 MiB | AAC 256k. Plays on essentially anything, including older phones and car stereos. |
| `mp3` | ~100 MiB | V0 VBR. Universal fallback; smaller than 320k CBR at the same perceived quality. |

```sh
dtx <url> --formats flac,opus       # or pick them in the interactive picker
dtx <url> --formats ""              # WAV only
```

A word on "without losing quality": only `wav` and `flac` are lossless. `opus`,
`m4a` and `mp3` are *transparent* — you will not hear the difference on playback
— but they are lossy, and their artifacts compound if someone re-sums the stems
in a DAW. Send `opus` to people who will play along; send `flac` to people who
will produce with it.

The compressed copies are encoded from the finished WAVs, so every format
carries identical source audio and differs only in codec.

## Why the files look like that

These constraints come from the DTX-PRO manuals, and are encoded in
`internal/dtxspec` rather than scattered through the code:

| Requirement | Value |
|---|---|
| Format | Linear PCM WAV |
| Sample rate | 44.1 kHz |
| Bit depth | 16-bit |
| Channels | Stereo |
| File names | Alphanumeric only — the module renders nothing else |
| Location | Root directory of the USB drive, never a subfolder |
| Limits | 1,000 `.wav` files; 90 minutes per audio song |

That naming rule drives three transformations, all in `internal/dtxspec`:

- **Accents fold to ASCII**, so `Ação de Graças` becomes `AcaoDeGracas` rather
  than losing the letters entirely. Apostrophes close up rather than splitting a
  word: `What's Up` is `WhatsUp`, not `WhatSUp`.
- **Production boilerplate is stripped.** `(Official Music Video)` costs 21 of
  the available characters and tells a drummer nothing, so it goes. `(Live)`,
  `(Acoustic)` and `(Remix)` change how you'd play the track, so they stay.
- **Every file in a run shares one base.** Names are decided for the set as a
  whole, not one at a time, so the suffix is always what differs:

```
4NonBlondesWhatsUpFull.wav       ← not 4NonBlondesWhatSUpOfFull.wav
4NonBlondesWhatsUpNoDrums.wav    ← not 4NonBlondesWhatSUNoDrums.wav
4NonBlondesWhatsUpNoVocals.wav   ← not 4NonBlondesWhatSNoVocals.wav
```

Shrinking each name to fit its own suffix would give every file a different
base, which is hard to scan on a small display and sorts badly.

## Install

```sh
go install github.com/eduardolopes/dtx@latest
```

Or from a clone:

```sh
make install     # builds and installs to $GOPATH/bin
```

### Dependencies

`ffmpeg`, `ffprobe` and `yt-dlp` must be on your PATH:

```sh
brew install ffmpeg yt-dlp
```

Demucs is Python, and `dtx` manages it for you in an isolated environment
(via [uv](https://github.com/astral-sh/uv)) so it never collides with any Python
you use for your own work:

```sh
dtx doctor            # show what is and is not installed
dtx doctor --install  # set up Demucs (~2 GB; pulls in PyTorch)
dtx doctor --uninstall
```

If you already have `demucs` on your PATH, `dtx` uses that instead and leaves
your setup alone.

> **Note:** demucs 4.1.0 imports `numpy` but omits it from its package metadata,
> and torch no longer installs it transitively. A plain `pip install demucs`
> therefore succeeds and then fails at runtime with `ModuleNotFoundError`.
> `dtx doctor --install` installs it explicitly and runs demucs once afterwards
> to prove the environment works, rather than discovering the problem minutes
> into a pipeline run.

## Usage

```sh
dtx                                  # interactive: prompts for URL and quality
dtx <url>                            # straight to work
dtx prep <url> --model htdemucs_ft   # slower, cleaner separation
dtx prep <url> --usb /Volumes/DTX    # copy results to a USB drive when done
```

### Useful flags

| Flag | Effect |
|---|---|
| `-o, --out` | Where results go (default `~/Music/dtx`) |
| `-m, --model` | `htdemucs` (default), `htdemucs_ft` (cleaner, ~4× slower), `htdemucs_6s` (adds guitar and piano) |
| `--device` | `auto`, `cpu`, `mps`, `cuda`. `auto` uses the GPU on Apple Silicon and falls back to CPU if that fails |
| `--shifts` | Extra quality passes; each one costs a full pass in time |
| `--normalize` | Loudness-normalize mixes to −14 LUFS |
| `--limit` | Brickwall limiter, if a mix clips |
| `--formats` | Extra formats to produce: `flac`, `opus`, `m4a`, `mp3`. WAV is always included |
| `--usb` | Copy finished files to a drive's root (WAVs only) |
| `--cookies-from-browser` | Borrow cookies for age-restricted videos |
| `-y, --yes` | Never prompt — for scripts and CI |
| `--plain` | Line-based output instead of the live view |

`htdemucs_6s` is worth knowing about: it yields `NoGuitar` and `NoPiano` mixes
too. The extra stems are experimental and weaker than the core four.

### Re-runs are cheap

The download and the separated stems are cached in the track's folder.
Re-running the same URL — or resuming after a cancelled run — skips straight
past the expensive parts and re-renders only what it needs to.

## Configuration

Settings resolve in this order, each overriding the last: defaults → config file
→ `DTX_`-prefixed environment variables → command-line flags.

```sh
dtx config show     # what is in effect right now, and where it came from
dtx config path     # where the config file lives
dtx config init     # write one, preloaded with current settings
```

```yaml
# ~/.config/dtx/config.yaml
output_dir: ~/Music/dtx
model: htdemucs
device: auto
formats: [flac]
normalize: false
usb_path: /Volumes/DTX
```

```sh
DTX_MODEL=htdemucs_ft dtx <url>
```

## A note on mixing

Minus-one mixes are summed with ffmpeg's `amix` using `normalize=0`. That matters:
`amix` divides by the number of inputs by default, which would make every mix
several dB quieter than the source. Demucs stems sum back to the original
recording, so a straight sum is the faithful result. Reach for `--limit` if a
particular track clips.

## Development

```sh
make test        # race detector + coverage
make lint        # vet + gofmt check
make build
```

The external tools are all driven through one `runner.Runner` interface, so the
whole pipeline is tested end to end against a fake — no network, no ffmpeg, no
Demucs required to run the suite.

```
internal/
├── dtxspec/    the module's requirements: format, limits, naming rules
├── runner/     external process execution, streaming output, cancellation
├── youtube/    yt-dlp
├── audio/      ffmpeg and ffprobe
├── separate/   Demucs
├── deps/       tool detection and Demucs provisioning
├── pipeline/   orchestration
├── config/     defaults, file, env, flags
└── ui/         Bubble Tea progress view, Lip Gloss styling, Huh prompts
```

## Sources

Audio requirements are taken from Yamaha's official documentation:

- [DTX-PRO Owner's Manual](https://usa.yamaha.com/files/download/other_assets/6/1353206/DTX-PRO_owners_manual_En_C0.pdf)
- [DTX-PRO Reference Manual](https://data.yamaha.com/files/download/other_assets/5/1378935/dtx_pro_en_rm_a0.pdf)
