package reaper

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ETLopes/cli/internal/daw"
	"github.com/ETLopes/cli/internal/studio"
)

//go:embed bridge.lua
var bridgeScript []byte

// Protocol constants, mirrored in bridge.lua.
const (
	extSection = "clistudio"
	// fieldSep is ASCII unit separator: it cannot appear in a track name, so
	// no argument needs escaping.
	fieldSep = "\x1f"
	// bridgePoll is how often the response slot is checked. The bridge polls
	// at 50ms, so anything faster only adds HTTP traffic.
	bridgePoll = 40 * time.Millisecond
	// bridgeTimeout bounds a single operation. Setup does the most work and
	// still finishes well inside this.
	bridgeTimeout = 10 * time.Second

	// tagPrefix marks a plugin instance this program manages.
	tagPrefix = "cs:"

	// bridgeActionID is the command ID the bridge is registered under in
	// reaper-kb.ini. It is fixed rather than generated so an upgrade reuses
	// the same registration instead of accumulating duplicates.
	bridgeActionID = "RS7c1152d9ab3e4f60"
	// bridgeCommand invokes that action; REAPER prefixes script command IDs
	// with an underscore.
	bridgeCommand = "_" + bridgeActionID
)

// Adapter drives REAPER, implementing daw.DAW.
type Adapter struct {
	client *Client
	seq    atomic.Uint64

	// gate admits one bridge call at a time. The protocol has a single
	// request slot and a single response slot in REAPER's extended state, so
	// two calls in flight together overwrite each other's request and the
	// loser waits for a reply that will never come. The mixer produces
	// exactly that whenever an arrow key is held down.
	gateOnce sync.Once
	gate     chan struct{}
}

// acquire takes the single-call gate, respecting cancellation so a caller that
// gives up waiting is not left blocked.
func (a *Adapter) acquire(ctx context.Context) error {
	a.gateOnce.Do(func() { a.gate = make(chan struct{}, 1) })
	select {
	case a.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) release() { <-a.gate }

// New returns an adapter for a REAPER web interface.
func New(host string, port int) *Adapter {
	return &Adapter{client: NewClient(host, port)}
}

// Ping confirms REAPER is reachable and its bridge is loaded.
func (a *Adapter) Ping(ctx context.Context) (daw.Info, error) {
	if _, err := a.client.Ping(ctx); err != nil {
		return daw.Info{}, err
	}
	version, err := a.call(ctx, "ping")
	if err != nil {
		return daw.Info{}, err
	}
	return daw.Info{Name: "REAPER", Version: version}, nil
}

// BridgeNotLoadedError reports that REAPER answered but the bridge script is
// not running, which is a distinct and separately fixable problem from REAPER
// being unreachable.
type BridgeNotLoadedError struct{}

func (e *BridgeNotLoadedError) Error() string {
	return "REAPER is reachable but the cli-studio bridge did not answer.\n\n" +
		"  Run `cli studio install`, then restart REAPER so it picks up the\n" +
		"  registered action."
}

// call performs one bridge operation and returns its payload.
//
// The request carries a sequence number that the bridge echoes back, so a
// stale response left in extended state by an earlier run is never mistaken
// for the answer to this one.
func (a *Adapter) call(ctx context.Context, op string, args ...string) (string, error) {
	return a.callWithTimeout(ctx, bridgeTimeout, op, args...)
}

// callWithTimeout is call with an explicit deadline, so tests need not wait
// the full production timeout to observe a non-responding bridge.
func (a *Adapter) callWithTimeout(ctx context.Context, timeout time.Duration, op string, args ...string) (string, error) {
	if err := a.acquire(ctx); err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	defer a.release()

	seq := strconv.FormatUint(a.seq.Add(1), 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	payload := strings.Join(append([]string{seq, op}, args...), fieldSep)

	if err := a.client.SetExtState(ctx, extSection, "req", payload); err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	// The bridge is a one-shot action: writing the request does nothing until
	// REAPER is told to run it.
	if err := a.client.RunAction(ctx, bridgeCommand); err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(bridgePoll)
	defer ticker.Stop()

	for {
		raw, err := a.client.GetExtState(ctx, extSection, "resp")
		if err != nil {
			return "", fmt.Errorf("%s: %w", op, err)
		}
		if strings.HasPrefix(raw, seq+fieldSep) {
			fields := strings.SplitN(raw, fieldSep, 3)
			if len(fields) < 2 {
				return "", fmt.Errorf("%s: malformed response from bridge", op)
			}
			var body string
			if len(fields) == 3 {
				body = fields[2]
			}
			if fields[1] == "err" {
				return "", fmt.Errorf("%s: %s", op, body)
			}
			return body, nil
		}

		if time.Now().After(deadline) {
			// No echo at all means nothing is listening, which points at the
			// script rather than at this operation.
			if raw == "" {
				return "", &BridgeNotLoadedError{}
			}
			return "", fmt.Errorf("%s: bridge did not respond within %s", op, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// effectTag is the name a managed plugin instance is renamed to, mirroring
// fx_tag in the bridge.
func effectTag(effectID string) string { return tagPrefix + effectID }

// roleOf is the stable identifier written into each managed track. Tracks are
// found by this tag rather than by index or name, so reordering or renaming a
// track in REAPER does not detach it from the studio.
func roleOf(id string) string { return id }

// Setup reconciles REAPER's topology with the studio's.
func (a *Adapter) Setup(ctx context.Context) (daw.SetupReport, error) {
	var instruments []string
	for _, in := range studio.Instruments() {
		instruments = append(instruments,
			fmt.Sprintf("%s:%s:%d", roleOf(in.ID), in.Name, in.Input))
	}

	var buses []string
	for _, b := range studio.Buses() {
		// The bridge works in zero-based hardware channels.
		buses = append(buses,
			fmt.Sprintf("%s:%s:%d", b.ID, b.Name, b.Output.Left-1))
	}

	raw, err := a.call(ctx, "setup",
		strings.Join(instruments, ","), strings.Join(buses, ","))
	if err != nil {
		return daw.SetupReport{}, err
	}

	var report daw.SetupReport
	for _, entry := range strings.Split(raw, fieldSep) {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|", 3)
		action := daw.Action{Kind: parts[0]}
		if len(parts) > 1 {
			action.Object = parts[1]
		}
		if len(parts) > 2 {
			action.Detail = parts[2]
		}
		report.Actions = append(report.Actions, action)
	}
	return report, nil
}

// SetSendLevel sets one instrument's send into one cue mix.
func (a *Adapter) SetSendLevel(ctx context.Context, cueID int, instrumentID string, level studio.Level) error {
	bus, ok := studio.LookupCue(cueID)
	if !ok {
		return fmt.Errorf("cue %d does not exist", cueID)
	}
	in, ok := studio.LookupInstrument(instrumentID)
	if !ok {
		return fmt.Errorf("unknown instrument %q", instrumentID)
	}
	_, err := a.call(ctx, "setsend", bus.ID, roleOf(in.ID),
		strconv.FormatFloat(float64(level), 'f', 2, 64))
	return err
}

// SetEffect enables or disables an effect, adding the plugin if needed.
func (a *Adapter) SetEffect(ctx context.Context, instrumentID, effectID string, enabled bool) error {
	in, ok := studio.LookupInstrument(instrumentID)
	if !ok {
		return fmt.Errorf("unknown instrument %q", instrumentID)
	}
	eff, ok := studio.LookupEffect(in.ID, effectID)
	if !ok {
		return fmt.Errorf("%s has no effect %q", in.ID, effectID)
	}
	var initial []string
	for _, p := range eff.Initial {
		initial = append(initial, fmt.Sprintf("%s=%g", p.Name, p.Value))
	}
	_, err := a.call(ctx, "setfx", roleOf(in.ID), eff.Plugin,
		boolArg(enabled), boolArg(eff.ShowsUI), strings.Join(initial, ","), eff.ID)
	return err
}

// SetTuning writes a pitch-correction setting into the plugin's state.
//
// Depth rides the Wet parameter, which is one of the three ReaTune does
// expose. Anything under full wet blends untouched voice back in, which is
// exactly what softens the snap, so an aggressive preset must be fully wet.
func (a *Adapter) SetTuning(ctx context.Context, instrumentID, effectID string, t studio.Tuning) error {
	in, ok := studio.LookupInstrument(instrumentID)
	if !ok {
		return fmt.Errorf("unknown instrument %q", instrumentID)
	}
	eff, ok := studio.LookupEffect(in.ID, effectID)
	if !ok {
		return fmt.Errorf("%s has no effect %q", in.ID, effectID)
	}
	chunk, err := TuningChunk(t)
	if err != nil {
		return err
	}
	if _, err := a.call(ctx, "setfxconfig", roleOf(in.ID), effectTag(eff.ID),
		"vst_chunk", chunk); err != nil {
		return err
	}
	_, err = a.call(ctx, "setfxparam", roleOf(in.ID), effectTag(eff.ID),
		"Wet", strconv.FormatFloat(t.Depth, 'f', 4, 64))
	return err
}

// SetChannel applies a whole channel strip.
//
// The strip's tone and dynamics are the channel's own EQ and compressor, not a
// second set: a desk has one of each per channel, and adding duplicates would
// stack two EQs on the same signal.
func (a *Adapter) SetChannel(ctx context.Context, instrumentID string, c studio.Channel) error {
	in, ok := studio.LookupInstrument(instrumentID)
	if !ok {
		return fmt.Errorf("unknown instrument %q", instrumentID)
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%s: %w", in.ID, err)
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	_, err := a.call(ctx, "setchannel", roleOf(in.ID),
		f(float64(c.Trim)), f(float64(c.EQ.High)), f(float64(c.EQ.Mid)),
		f(c.EQ.MidFreq), f(float64(c.EQ.Low)), f(float64(c.CompThreshold())),
		f(c.Pan), f(float64(c.Fader)), boolArg(c.Muted), boolArg(c.Soloed))
	return err
}

// SetMonitorVolume sets the control-room level.
func (a *Adapter) SetMonitorVolume(ctx context.Context, level studio.Level) error {
	_, err := a.call(ctx, "setmonvol", strconv.FormatFloat(float64(level), 'f', 2, 64))
	return err
}

// SetMonitorMute mutes or unmutes the control room.
func (a *Adapter) SetMonitorMute(ctx context.Context, muted bool) error {
	_, err := a.call(ctx, "setmonmute", boolArg(muted))
	return err
}

// Save writes REAPER's project to disk.
func (a *Adapter) Save(ctx context.Context) error {
	_, err := a.call(ctx, "save")
	return err
}

// Snapshot reads REAPER's current state back into the domain model.
func (a *Adapter) Snapshot(ctx context.Context) (*studio.Session, error) {
	var cueIDs, instIDs []string
	for _, b := range studio.CueBuses() {
		cueIDs = append(cueIDs, b.ID)
	}
	for _, in := range studio.Instruments() {
		instIDs = append(instIDs, roleOf(in.ID))
	}

	raw, err := a.call(ctx, "snapshot",
		strings.Join(cueIDs, ","), strings.Join(instIDs, ","))
	if err != nil {
		return nil, err
	}

	session := studio.NewSession("reaper")
	for _, entry := range strings.Split(raw, fieldSep) {
		if entry == "" {
			continue
		}
		if err := applySnapshotEntry(session, entry); err != nil {
			return nil, err
		}
	}
	return session, nil
}

// applySnapshotEntry folds one reported value into the session.
//
// Unrecognised entries are skipped rather than failing the read: REAPER may
// hold plugins the studio does not manage, and their presence is not an error.
func applySnapshotEntry(s *studio.Session, entry string) error {
	f := strings.Split(entry, "|")
	switch f[0] {
	case "send":
		if len(f) < 4 {
			return nil
		}
		cue, err := cueNumber(f[1])
		if err != nil {
			return nil
		}
		db, err := strconv.ParseFloat(f[3], 64)
		if err != nil {
			return nil
		}
		_, err = s.SetCueLevel(cue, f[2], studio.Adjustment{Delta: studio.Level(db)})
		// A level REAPER holds may sit outside what the studio permits; report
		// it as the actual state rather than refusing to read it.
		if err != nil {
			return nil
		}
	case "monvol":
		if len(f) < 2 {
			return nil
		}
		if db, err := strconv.ParseFloat(f[1], 64); err == nil {
			_, _ = s.SetMonitorVolume(studio.Adjustment{Delta: studio.Level(db)})
		}
	case "monmute":
		s.SetMonitorMute(len(f) > 1 && f[1] == "1")
	case "fx":
		if len(f) < 4 {
			return nil
		}
		// Managed instances are tagged, which is the only way to tell apart
		// several copies of one plugin: a vocal chain holds three ReaTune
		// instances. Untagged instances are matched by plugin name so
		// anything created before tagging is still recognised.
		for _, eff := range studio.Chain(f[1]) {
			if f[2] == effectTag(eff.ID) {
				_ = s.SetEffect(f[1], eff.ID, f[3] == "1")
				break
			}
		}
		if !strings.HasPrefix(f[2], tagPrefix) {
			for _, eff := range studio.Chain(f[1]) {
				if strings.Contains(f[2], eff.Plugin) {
					_ = s.SetEffect(f[1], eff.ID, f[3] == "1")
					break
				}
			}
		}
	}
	return nil
}

func cueNumber(busID string) (int, error) {
	for _, b := range studio.CueBuses() {
		if b.ID == busID {
			return b.CueID, nil
		}
	}
	return 0, fmt.Errorf("unknown cue bus %q", busID)
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// --- bridge installation ---

// ResourceDir returns REAPER's user resource directory.
func ResourceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "REAPER")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "REAPER")
	default:
		return filepath.Join(home, ".config", "REAPER")
	}
}

// bridgeFileName is the installed script's name.
const bridgeFileName = "cli-studio-bridge.lua"

// InstallReport describes what installation did.
type InstallReport struct {
	ScriptPath   string
	RegistryPath string
	Actions      []string
}

// Install writes the bridge script into REAPER's Scripts directory and ensures
// REAPER loads it at startup.
//
// REAPER runs Scripts/__startup.lua automatically, which avoids asking the user
// to register an action ID by hand. An existing startup script is appended to
// rather than replaced, since it is the user's file and may do other work.
func Install() (InstallReport, error) {
	res := ResourceDir()
	if res == "" {
		return InstallReport{}, fmt.Errorf("cannot locate REAPER's resource directory")
	}
	if _, err := os.Stat(res); err != nil {
		return InstallReport{}, fmt.Errorf(
			"REAPER's resource directory is not at %s; start REAPER once so it is created", res)
	}

	scripts := filepath.Join(res, "Scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		return InstallReport{}, fmt.Errorf("creating Scripts directory: %w", err)
	}

	report := InstallReport{ScriptPath: filepath.Join(scripts, bridgeFileName)}

	existing, err := os.ReadFile(report.ScriptPath)
	switch {
	case err == nil && string(existing) == string(bridgeScript):
		report.Actions = append(report.Actions, "bridge script already current")
	default:
		if err := os.WriteFile(report.ScriptPath, bridgeScript, 0o644); err != nil {
			return report, fmt.Errorf("writing bridge script: %w", err)
		}
		report.Actions = append(report.Actions, "wrote "+bridgeFileName)
	}

	if err := registerAction(res, &report); err != nil {
		return report, err
	}
	cleanupStaleStartup(scripts, &report)
	return report, nil
}

// kbLine is the reaper-kb.ini entry that registers the bridge as a Main-section
// action, which is what gives it an invocable command ID.
func kbLine() string {
	return fmt.Sprintf("SCR 4 0 %s \"Custom: cli studio bridge\" %s",
		bridgeActionID, bridgeFileName)
}

// registerAction adds the bridge to REAPER's action registry.
//
// REAPER's only startup hook is __startup.eel, which cannot load a Lua file,
// so the script is registered as an action and invoked by command ID instead.
// REAPER reads this file at startup and rewrites it on exit, so the entry is
// added while REAPER is closed and takes effect on the next launch.
func registerAction(resourceDir string, report *InstallReport) error {
	path := filepath.Join(resourceDir, "reaper-kb.ini")
	report.RegistryPath = path
	line := kbLine()

	current, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		report.Actions = append(report.Actions, "registered the bridge action")
		return nil
	case err != nil:
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if strings.Contains(string(current), bridgeActionID) {
		report.Actions = append(report.Actions, "bridge action already registered")
		return nil
	}

	// Someone else's keymap: append a line, never rewrite the file.
	body := string(current)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body+line+"\n"), 0o644); err != nil {
		return fmt.Errorf("updating %s: %w", path, err)
	}
	report.Actions = append(report.Actions, "registered the bridge action")
	return nil
}

// staleStartup is the loader an earlier version of this tool installed, before
// it was established that REAPER has no __startup.lua hook. It never ran, so
// it is removed rather than left to puzzle whoever finds it.
const staleStartup = "-- REAPER startup script\ndofile(reaper.GetResourcePath() .. \"/Scripts/cli-studio-bridge.lua\")\n"

func cleanupStaleStartup(scriptsDir string, report *InstallReport) {
	path := filepath.Join(scriptsDir, "__startup.lua")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Only remove the file if it is exactly what this tool wrote; anything
	// else belongs to the user.
	if string(body) != staleStartup {
		return
	}
	if err := os.Remove(path); err == nil {
		report.Actions = append(report.Actions, "removed the unused __startup.lua")
	}
}

// Ensure the adapter satisfies the interface the domain depends on.
var _ daw.DAW = (*Adapter)(nil)
