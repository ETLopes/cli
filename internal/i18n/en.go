package i18n

// en is the source catalogue. Every other language is measured against it, so
// a key added here without a translation elsewhere is reported by a test.
var en = map[string]string{
	// Control explanations. These are written in terms of what will be heard
	// rather than what the parameter is called: "threshold in decibels" means
	// nothing to someone who does not already understand compression.
	"ctrl.trim": "Input level before anything else. Raise it if a quiet source is barely " +
		"registering, lower it if the signal is distorting. Set this first: everything " +
		"below reacts to what it is fed.",
	"ctrl.high": "Treble. Boost for air, breath and string detail; cut to tame harshness, " +
		"cymbal splash or sibilance.",
	"ctrl.mid": "The band where most instruments live, so it decides what sounds present " +
		"and what sounds buried. Cutting here usually clears space; boosting brings a " +
		"source forward and can sound boxy or nasal.",
	"ctrl.midfreq": "Which frequency the MID control acts on. Sweep it while boosting to " +
		"find the offending note, then cut there. Low hundreds is boxiness, around 1k is " +
		"honk, 3-4k is bite and harshness.",
	"ctrl.low": "Bass. Boost for weight and body; cut to remove rumble, mic thump, or mud " +
		"where several instruments are fighting for the same low end.",
	"ctrl.comp": "How hard the compressor works. It makes loud parts quieter, which evens " +
		"out a performance and lets you raise the overall level. A little steadies a vocal " +
		"or bass; a lot flattens dynamics and starts to pump.",
	"ctrl.pan": "Where the channel sits between the left and right speakers. Spreading " +
		"sources apart stops them masking each other; keep bass, kick and lead vocal near " +
		"the centre.",
	"ctrl.mute": "Silences this channel everywhere, including the headphone mixes.",
	"ctrl.solo": "Hears this channel alone. While anything is soloed, everything not soloed " +
		"goes quiet. Useful for finding a problem; easy to leave on by mistake.",
	"ctrl.fader": "The channel's level in the main mix. It does not affect the headphone " +
		"mixes: those are taken before the fader, so you can change the room mix without " +
		"touching what anyone is hearing.",
	"ctrl.aux": "How much of this channel goes to headphone mix %d. Each mix is what one " +
		"musician hears, independent of the others and of the main fader.",

	// Console.
	"console.keys": "←/→ channel · ↑/↓ control · +/- adjust (shift ×4) · space toggle · " +
		"? help · tab page · s save · q quit",
	"console.showing":    "showing %d-%d of %d inputs",
	"console.no_inputs":  "no inputs configured",
	"console.limit":      "%s is at its limit",
	"console.saved":      "saved %s",
	"console.connecting": "reconnecting...",

	// Studio status.
	"studio.connected":     "connected to %s",
	"studio.disconnected":  "REAPER not connected",
	"studio.matches":       "REAPER matches the session",
	"studio.differences":   "%d difference(s) from the session:",
	"studio.sync_hint":     "Run 'cli studio sync' to make REAPER match.",
	"studio.saved_session": "saved to the session; run 'cli studio sync' once REAPER is available.",

	// Setup.
	"setup.unchanged": "already set up; nothing to change",
	"setup.summary":   "%d created, %d repaired, %d unchanged",
	"setup.restart":   "Restart REAPER so it loads the bridge.",
	"setup.next":      "Next: start REAPER, then run 'cli studio init' again.",
	"setup.next_hint": "That second run builds the tracks, buses and routing.",

	// Monitoring.
	"monitor.muted":   "monitors muted",
	"monitor.unmuted": "monitors unmuted",

	// Command descriptions.
	"cmd.studio.short":  "Control a REAPER-based home studio",
	"cmd.dtx.short":     "Turn any video into DTX-PRO-ready drum practice tracks",
	"cmd.cue.short":     "Set an instrument's level in a headphone mix",
	"cmd.init.short":    "Set up REAPER from scratch: web interface, bridge, topology",
	"cmd.setup.short":   "Create or repair the studio topology in REAPER",
	"cmd.status.short":  "Show the connection and any drift from the session",
	"cmd.sync.short":    "Make REAPER match the session",
	"cmd.monitor.short": "Control the control-room monitors",

	// Toolbox.
	"toolbox.tagline":       "a personal toolbox",
	"toolbox.launcher_keys": "↑/↓ or j/k to move · enter to run · 1-9 to jump · q to quit",
	"tool.dtx.short":        "Turn a video into DTX-PRO drum practice tracks",
	"tool.studio.short":     "Drive a REAPER home studio: cue mixes, effects, monitoring",

	// Settings editor.
	"set.group.general": "General",
	"set.group.studio":  "Studio",
	"set.group.dtx":     "DTX practice tracks",
	"set.lang":          "Language",
	"set.lang.help":     "The language everything is shown in. Takes effect immediately.",
	"set.reaper_host":   "REAPER host",
	"set.reaper_host.help": "Where REAPER's web interface listens. Leave at 127.0.0.1 unless " +
		"REAPER runs on another machine: its web interface has no password by default.",
	"set.reaper_port":      "REAPER port",
	"set.reaper_port.help": "Must match the port set in REAPER's Preferences under Control/OSC/web.",
	"set.session":          "Session",
	"set.session.help":     "Which saved session opens by default.",
	"set.session_dir":      "Session folder",
	"set.session_dir.help": "Where session files are kept.",
	"set.model":            "Separation model",
	"set.model.help": "Which model splits a track into stems. Better separation costs " +
		"proportionally more time.",
	"set.model.standard":  "4 stems, fastest",
	"set.model.finetuned": "4 stems, cleanest, about 4x slower",
	"set.model.sixstem":   "adds guitar and piano, experimental",
	"set.device":          "Compute device",
	"set.device.help": "What does the separation work. Auto picks the GPU on Apple Silicon " +
		"and falls back to the processor if that fails.",
	"set.device.auto":     "auto — GPU if available",
	"set.formats":         "Share formats",
	"set.formats.help":    "Extra formats produced alongside the module WAVs. Choose from: %s",
	"set.output_dir":      "Output folder",
	"set.output_dir.help": "Where finished tracks are written.",
	"set.normalize":       "Normalise loudness",
	"set.normalize.help": "Evens the loudness of every rendered mix. Changes the dynamics of " +
		"the recording, so it is off unless a mix feels quiet next to the others.",
	"set.limit":         "Limiter",
	"set.limit.help":    "Prevents clipping when stems are summed. Rarely needed.",
	"set.usb_path":      "USB drive",
	"set.usb_path.help": "Copy finished WAVs here when a run completes. Leave unset to skip.",
	"set.on":            "on",
	"set.off":           "off",
	"set.unset":         "not set",
	"set.err.number":    "must be a number",
	"set.keys":          "↑/↓ setting · ←/→ change · enter edit · s save · q quit",
	"set.keys.editing":  "type to edit · enter confirm · esc cancel",
	"set.saved":         "saved",
	"set.unsaved":       "unsaved changes",
	"set.title":         "settings",

	"cmd.levels.short": "Show what each input is receiving",
	"levels.title":     "LEVELS",
	"levels.keys":      "q quit · gain knobs are on the interface",
	"meter.silent":     "nothing arriving",
	"meter.low":        "low — raise the preamp",
	"meter.good":       "good",
	"meter.hot":        "hot — lower the preamp",

	"cmd.phones.short": "Set an instrument's level in a headphone jack on the interface",

	"phones.title":  "PHONES %d",
	"phones.feeds":  "outputs %s \u00b7 also %s",
	"phones.nocues": "no cue mix reaches this jack",

	// Input patching.
	"patch.input":     "input",
	"patch.inputs":    "inputs",
	"patch.mono":      "mono",
	"patch.stereo":    "stereo",
	"patch.conflict":  "clashes with %s",
	"patch.free":      "free inputs",
	"patch.keys":      "↑/↓ input · ←/→ channel · t instrument · a add · d remove · m mono/stereo · s save · q quit",
	"patch.saved":     "saved and applied to REAPER",
	"patch.unsaved":   "unsaved — press s to apply",
	"patch.conflicts": "fix the clashes before saving",
	"patch.nowis":     "now %s — its effects change with it",
	"patch.added":     "added on input %d",
	"patch.removed":   "unplugged %s",
	"patch.full":      "all %d inputs are in use",
	"patch.last":      "the studio needs at least one input",

	// What is plugged into an input, which decides its effect chain.
	"kind.vocal":  "Vocal",
	"kind.guitar": "Guitar",
	"kind.bass":   "Bass",
	"kind.keys":   "Keys",
	"kind.drums":  "Drums",
	"kind.line":   "Line",
}
