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
}
