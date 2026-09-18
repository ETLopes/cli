# cli

[![Release](https://img.shields.io/github/v/release/ETLopes/cli?logo=github)](https://github.com/ETLopes/cli/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ETLopes/cli?logo=go)](go.mod)
[![License](https://img.shields.io/github/license/ETLopes/cli)](LICENSE)

A personal toolbox of day-to-day tools. Each tool lives under its own
subcommand and works interactively when run without arguments.

| Tool | What it does |
|---|---|
| `cli dtx` | Turn any video into practice tracks a Yamaha DTX-PRO drum module will play |
| `cli studio` | Drive a REAPER home studio: cue mixes, effects and monitoring |

## Install

```sh
go install github.com/ETLopes/cli@latest
```

Or grab a prebuilt binary for your platform from the
[latest release](https://github.com/ETLopes/cli/releases/latest).

Building from a clone:

```sh
make install     # builds and installs to $GOPATH/bin
```

---

# cli dtx

Give it a URL and it downloads the audio, splits it into instrument stems with
[Demucs](https://github.com/adefossez/demucs), and renders everything as the
44.1 kHz / 16-bit / stereo WAV the DTX-PRO requires — including a **"minus-one"
mix for every instrument**, so you can mute the drums and play them yourself.

```
$ cli dtx https://www.youtube.com/watch?v=...

cli  preparing practice tracks

  ✓ Inspecting    Some Song (Official Video)
  ✓ Downloading   source.m4a
  ⠸ Separating    ████████████░░░░░░░░  62%  htdemucs
    Rendering
    Encoding
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
cli dtx <url> --formats flac,opus   # or pick them in the interactive picker
cli dtx <url> --formats ""          # WAV only
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

## Dependencies

`ffmpeg`, `ffprobe` and `yt-dlp` must be on your PATH:

```sh
brew install ffmpeg yt-dlp
```

Demucs is Python, and `dtx` manages it for you in an isolated environment
(via [uv](https://github.com/astral-sh/uv)) so it never collides with any Python
you use for your own work:

```sh
cli dtx doctor            # show what is and is not installed
cli dtx doctor --install  # set up Demucs
cli dtx doctor --uninstall
```

If you already have `demucs` on your PATH, `dtx` uses that instead and leaves
your setup alone.

> **Note:** demucs 4.1.0 imports `numpy` but omits it from its package metadata,
> and torch no longer installs it transitively. A plain `pip install demucs`
> therefore succeeds and then fails at runtime with `ModuleNotFoundError`.
> `cli dtx doctor --install` installs it explicitly and runs demucs once afterwards
> to prove the environment works, rather than discovering the problem minutes
> into a pipeline run.

## Usage

```sh
cli dtx                                  # interactive: URL, quality, formats
cli dtx <url>                            # straight to work
cli dtx prep <url> --model htdemucs_ft   # slower, cleaner separation
cli dtx prep <url> --usb /Volumes/DTX    # copy results to a USB drive when done
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
cli config show     # what is in effect right now, and where it came from
cli config path     # where the config file lives
cli config init     # write one, preloaded with current settings
```

Each tool owns a section, so two tools can both have an `output_dir` without
colliding.

```yaml
# ~/.config/cli/config.yaml
dtx:
  output_dir: ~/Music/dtx
  model: htdemucs
  device: auto
  formats: [flac]
  normalize: false
  usb_path: /Volumes/DTX
```

```sh
CLI_DTX_MODEL=htdemucs_ft cli dtx <url>
```

## A note on mixing

Minus-one mixes are summed with ffmpeg's `amix` using `normalize=0`. That matters:
`amix` divides by the number of inputs by default, which would make every mix
several dB quieter than the source. Demucs stems sum back to the original
recording, so a straight sum is the faithful result. Reach for `--limit` if a
particular track clips.

---

# cli studio

Drives a REAPER-based home studio in studio terms rather than DAW terms:
instruments, headphone mixes, effects and monitoring. One track per physical
input, and headphone mixes built from sends off those tracks, so changing the
control-room mix never alters what a musician hears.

```
$ cli studio

  STUDIO  rehearsal   ● REAPER 7.80/macOS-arm64

   1 CONSOLE  2 INPUTS  3 CUE 1  4 CUE 2  5 CUE 3  6 CUE 4  7 CUE 5
   8 CUE 6  9 CUE 7  CUE 8  FX  MONITOR

   ▸ Mic 1       +1 dB    ────────┆──┃──────────
     Guitar      +3 dB    ────────┆────┃────────
     Bass        -2 dB    ──────┃─┆──────────────
```

## The rig it assumes

| | |
|---|---|
| Interface | Focusrite Scarlett 18i20 |
| Monitors | Yamaha HS5 on outputs 1/2 |
| Headphone amp | Mackie HM-800, eight inputs on outputs 3–10, one per channel |
| Inputs | Mic 1, Mic 2, Guitar, Bass, Keyboard on 1–5; DTX drums on 7 |

Keyboard and DTX are mono, as wired. The HM-800's inputs are mono, so each
cue is one output rather than a pair — eight cues out of the same ten
channels four stereo cues would have used. Every instrument sends to every
cue, which is the point: eight musicians, eight independent mixes.

The topology lives in `internal/studio/topology.go`; a different rig is a
config file, below.

## Setup

From a fresh REAPER, with REAPER **closed**:

```sh
cli studio init     # enables the web interface, installs the bridge
# start REAPER
cli studio init     # builds the tracks, buses and routing
```

REAPER's web interface is off by default and is the only way in from outside,
so the first run switches it on by editing REAPER's own configuration. That
file is rewritten when REAPER exits, which is why REAPER has to be closed for
it. Any control surface already configured is kept, not replaced.

The second run needs REAPER open, and is the same work `cli studio setup`
does: it reuses what exists, repairs routing that has drifted, and never
touches tracks it does not manage. Managed objects are tagged, so renaming or
reordering tracks in REAPER does not detach them.

Why a bridge script at all: REAPER's web interface can read and write state
but cannot create tracks or assign hardware inputs, while ReaScript can do
both but is unreachable from outside REAPER. The bridge closes the gap.

## Describing a different rig

The inputs and outputs above are defaults, not assumptions. `cli config init`
writes them out to edit:

```yaml
studio:
  inputs:
    - {id: mic1,    name: Mic 1,    channel: 1, kind: vocal}
    - {id: guitar,  name: Guitar,   channel: 3, kind: guitar}
    - {id: guitar2, name: Guitar 2, channel: 4, kind: guitar}
    - {id: keys,    name: Keys,     channel: 5, kind: keys, mode: stereo}
  outputs:
    main: [1, 2]
    cues: [[3], [4], [5, 6]]
```

`id` is what commands use (`cli studio guitar2 overdrive on`), `channel` counts
from 1, and `mode` defaults to mono — a stereo input claims the next channel
too. Cue mixes are numbered in the order listed. A cue is one channel for a
mono headphone amp input, or two for a stereo one; MAIN is always a pair.

`kind` is what is plugged in — `vocal`, `guitar`, `bass`, `keys`, `drums` or
`line` — and it decides the input's effect chain. Two inputs of the same kind
get the same pedalboard as separate instances on separate tracks, so the two
guitars above each have their own amp, drive and delay. Omit it and it is
inferred from the ID, which is why a file written before kinds existed keeps
working untouched.

A config file wins over the defaults, so a rig written before a default
changed keeps the outputs it was given until that block is edited.

A rig is checked before it is used: two inputs on one channel, or two buses on
one output, is refused rather than discovered through the speakers. A session
written against a different rig still opens, reporting what it had to drop.

## Changing what is plugged in

The INPUTS page edits the rig from inside `cli studio`, without a text editor:

| Key | |
|---|---|
| `↑` `↓` | choose an input |
| `←` `→` | move it to another channel |
| `t` | change what is plugged in, which changes its pedalboard |
| `a` `d` | add an input, unplug one |
| `m` | mono or stereo |
| `s` | save and apply to REAPER |

Retyping an input renames it to suit — a bass on input 4 becomes Guitar 2
beside the guitar already there — and the FX page follows, offering that input
a guitar's board. Cycling through the kinds and back leaves the instrument
exactly as it was, since its saved levels and effects are keyed by its ID.

Saving reconciles REAPER. A track the rig no longer names is removed, because
one left behind is still armed on its input and still feeding every cue, so
that signal would arrive twice. A track holding a recording is unwired and
kept instead: deleting a take is not a decision this makes for you.

## The console

`cli studio` opens on a console view: one strip per input, side by side, every
control in the same place on every strip. It is read by position rather than by
label, which is what makes a desk fast once your hands know it.

```
        MIC 1    MIC 2    GUITAR   BASS     KEYBOARD DTX
 TRIM   0        0        0        0        0        0
 HIGH   0        0        +3       0        0        0
 MID    0        0        -2       0        0        0
 FREQ   1.0k     1.0k     1.0k     1.0k     1.0k     1.0k
 LOW    0        0        +1       0        0        0
 COMP   0%       0%       35%      55%      0%       0%
 AUX1   0        0        +3       0        0        0
 AUX2   0        0        0        0        0        0
 PAN    C        C        L20      C        C        C
 MUTE   ·        ·        ·        ON       ·        ·
 SOLO   ·        ·        ·        ·        ·        ·
 ─────────────────────────────────────────────────────
 FADER  0        0        -3       -1       0        0
```

| Key | |
|---|---|
| `←` `→` | move between channels |
| `↑` `↓` | move between controls |
| `+` `-` | adjust (shift for four notches at once) |
| `space` | toggle mute and solo |
| `tab` | other pages: cue mixes, the pedalboards, monitoring |
| `1`–`9` | jump straight to a page |
| `s` | save |

The aux rows *are* the cue sends, so `AUX1` on the console and
`cli studio cue 1 guitar +3` move the same thing. How many aux rows appear
follows the rig: a desk has a fixed number of sends, and this does not.

Tone and dynamics drive the channel's own EQ and compressor rather than adding
a second set, so the console and the pedalboard cannot fight over the same
signal. `TRIM` is digital input gain — the preamp knobs on the interface are
analogue and out of reach from here.

## Cue mixes

A cue mix is what one musician hears. Sends are **pre-fader post-FX**, so they
carry the processed tone while staying independent of the main fader.

```sh
cli studio cue 1 guitar +3      three decibels louder
cli studio cue 1 bass -2        two decibels quieter
cli studio cue 2 guitar 0       set to unity
cli studio cue 3 keyboard @-6   set to exactly -6 dB
cli studio cue 1 dtx off        silence it
```

A signed level changes by that amount; unsigned is absolute. Since `-6` already
means "six quieter", an exact negative level is written `@-6`.

## Effects

Each instrument has a pedalboard in signal order — tuner first on a clean
signal, dynamics and dirt before the amp, modulation and time after it.

```sh
cli studio guitar overdrive on
cli studio bass compressor on
cli studio mic 1 t-pain on
```

| Instrument | Chain |
|---|---|
| Guitar | tuner · wah · octave ± · comp · overdrive · fuzz · saturation · clipper · gate · amp · EQ · chorus · flanger · phaser · tremolo · delay · ping-pong · reverb |
| Bass | tuner · gate · octave down · comp · 1175 · Major Tom · drive · saturation · clipper · amp · low boost · chorus · EQ |
| Mic 1 & 2 | tuner · gate · comp · de-esser · pitch correction · hard tune · harmony · EQ · delay · reverb · vocoder |
| Keyboard | comp · saturation · EQ · chorus · delay · lo-fi delay · reverb |
| DTX | gate · drum comp · EQ · room reverb |

Everything starts off. Effects load with settings that actually do something —
compressors open at −18 dB rather than REAPER's default 0 dBFS threshold, where
nothing ever crosses it.

Two exceptions worth knowing: the **amp** is a convolution modeler and stays
clean until you load a cabinet impulse response, and the **tuner** needs a
window size around 50 ms for voice or 100 ms for bass (a low E at 41 Hz does
not fit enough cycles in the stock 30 ms to be detected at all).

### Hard pitch correction

`hardtune` produces obvious, robotic pitch snapping, and answers to the alias
`t-pain` after the production style it imitates:

```sh
cli studio mic 1 t-pain on
cli studio mic 1 t-pain off
```

Switching it on configures everything: instant retune, fully wet, snapped to
A minor pentatonic. Five legal notes rather than twelve means wider gaps, so
the voice is forced further on every correction — that gap is the effect.

ReaTune exposes only three automatable parameters, so these settings are
written directly into the plugin's state blob. If a song is in another key:

```sh
cli studio tune mic1 --key C          # hidden; the defaults cover most cases
```

## Monitoring

```sh
cli studio monitor volume -3
cli studio monitor volume @-20
cli studio monitor mute
cli studio monitor unmute
```

Monitors cap at unity — boosting the speakers above the mix is never what
anyone means. New sessions start **muted at −20 dB**, because setup attaches
speakers of unknown volume to routing that did not exist a moment earlier.
A single relative change over ±12 dB is refused as a typo.

## Sessions and drift

The session file is the desired state; REAPER is the actual state. They are
compared rather than assumed equal.

```sh
cli studio status               # connection, and any drift from the session
cli studio sync                 # make REAPER match the session
cli studio sync --dry-run
cli studio session list|new|save|show
```

Edits are recorded even when REAPER is unreachable, and reported rather than
hidden, so nothing is lost because the DAW happened to be closed.

## Configuration

```yaml
# ~/.config/cli/config.yaml
studio:
  reaper_host: 127.0.0.1
  reaper_port: 8765
  session_dir: ~/.local/share/cli/studio/sessions
  session: default
```

```sh
CLI_STUDIO_REAPER_PORT=9080 cli studio status
```

The port is deliberately not REAPER's own default of 8080, which is the most
contested port on a development machine. REAPER's web interface has no
authentication by default, so it is worth setting a password in that same
preferences pane if your network is shared.

## Settings

`cli config` opens an editor. Every setting explains itself, values are
validated as you type, and nothing is written until you save.

```
cli  settings

General
  Language            English

Studio
  REAPER host         127.0.0.1
  REAPER port         8765

DTX practice tracks
▸ Separation model    ‹ htdemucs — 4 stems, fastest ›
  Compute device      auto — GPU if available

  Which model splits a track into stems. Better separation costs
  proportionally more time.

  ↑/↓ setting · ←/→ change · enter edit · s save · q quit
```

Or change one from the command line:

```sh
cli config set lang pt
cli config set studio.reaper_port 9080
cli config show                        # what is in effect, and where from
cli config init                        # write a file to edit by hand
```

Saving reads the existing file first, so settings the editor does not show are
left alone.

## Language

Everything the toolbox prints exists in English and Brazilian Portuguese.

```sh
CLI_LANG=pt cli studio
```

```yaml
# ~/.config/cli/config.yaml
lang: pt
```

The setting wins over `CLI_LANG`, which wins over the system locale — so if
your machine is already `pt_BR`, there is nothing to configure.

Console labels stay in English on purpose. `TRIM`, `PAN`, `MUTE`, `SOLO` and
`EQ` are printed that way on the panel of every desk sold in Brazil, so
translating them would make the layout less familiar rather than more. The
prose that explains each control is translated in full.

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
├── cli/        cobra command tree: the toolbox root and each tool
├── i18n/       message catalogues, one per language
├── studio/     the studio domain: topology, levels, chains, sessions
├── daw/        what the app needs from a workstation, in studio terms
├── reaper/     REAPER adapter: web interface client and ReaScript bridge
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

## License

[MIT](LICENSE) — Copyright (c) 2026 Eduardo Lopes.

`cli dtx` drives three external tools that carry their own licences:
[ffmpeg](https://ffmpeg.org) (LGPL/GPL depending on build),
[yt-dlp](https://github.com/yt-dlp/yt-dlp) (Unlicense) and
[Demucs](https://github.com/adefossez/demucs) (MIT). They are invoked as
separate processes, not linked in, and none of them ship inside the released
binaries.

## Sources

Audio requirements are taken from Yamaha's official documentation:

- [DTX-PRO Owner's Manual](https://usa.yamaha.com/files/download/other_assets/6/1353206/DTX-PRO_owners_manual_En_C0.pdf)
- [DTX-PRO Reference Manual](https://data.yamaha.com/files/download/other_assets/5/1378935/dtx_pro_en_rm_a0.pdf)
