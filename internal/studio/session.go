package studio

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// Monitor is the control-room output feeding the speakers.
type Monitor struct {
	Volume Level
	Muted  bool
}

// CueMix is what one musician hears: a send level per instrument.
//
// A cue mix never owns audio. It is a set of levels on sends taken from the
// single canonical track per instrument, which is why changing someone's
// headphone mix cannot disturb anyone else's.
type CueMix struct {
	ID     int
	levels map[string]Level
}

// Level returns the send level for an instrument.
func (c *CueMix) Level(instrumentID string) Level {
	return c.levels[normalizeID(instrumentID)]
}

// Levels returns every send level, keyed by instrument ID.
func (c *CueMix) Levels() map[string]Level {
	out := make(map[string]Level, len(c.levels))
	for k, v := range c.levels {
		out[k] = v
	}
	return out
}

// Session is the desired state of the studio: the levels, effects and
// monitoring the user has asked for. What the DAW currently does is a separate
// question, answered by comparing against this.
type Session struct {
	Name    string
	monitor Monitor
	cues    map[int]*CueMix
	// fx maps instrument ID to effect ID to enabled.
	fx map[string]map[string]bool
}

// NewSession builds a session with safe defaults.
//
// Monitoring starts muted and quiet. Someone running setup for the first time
// has speakers of unknown volume attached to a routing configuration that did
// not exist a moment ago, and the cost of being wrong is a very loud noise.
// Unmuting is one command; hearing damage is not undoable.
func NewSession(name string) *Session {
	s := &Session{
		Name:    name,
		monitor: Monitor{Volume: SafeStartupLevel, Muted: true},
		cues:    make(map[int]*CueMix, CueCount()),
		fx:      make(map[string]map[string]bool),
	}

	for _, bus := range CueBuses() {
		levels := make(map[string]Level, len(instruments))
		for _, in := range instruments {
			// Unity is the useful starting point for a rehearsal: everyone
			// hears everyone, and mixes are dialled in from there.
			levels[in.ID] = Unity
		}
		s.cues[bus.CueID] = &CueMix{ID: bus.CueID, levels: levels}
	}

	for _, in := range instruments {
		enabled := make(map[string]bool)
		for _, e := range Chain(in.ID) {
			enabled[e.ID] = e.DefaultOn
		}
		s.fx[in.ID] = enabled
	}
	return s
}

// Monitor returns the control-room state.
func (s *Session) Monitor() Monitor { return s.monitor }

// Cue returns a cue mix by number.
func (s *Session) Cue(id int) (*CueMix, error) {
	c, ok := s.cues[id]
	if !ok {
		return nil, fmt.Errorf("cue %d does not exist (available: 1-%d)", id, CueCount())
	}
	return c, nil
}

// Cues returns every cue mix, ordered by number.
func (s *Session) Cues() []*CueMix {
	out := make([]*CueMix, 0, len(s.cues))
	for _, c := range s.cues {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Change records a level moving from one value to another, so callers can log
// and display what actually happened rather than what was asked for.
type Change struct {
	From, To Level
}

// Changed reports whether the level actually moved.
func (c Change) Changed() bool { return c.From != c.To }

func (c Change) String() string { return c.From.String() + " -> " + c.To.String() + " dB" }

// SetCueLevel adjusts one instrument's send into one cue mix.
func (s *Session) SetCueLevel(cueID int, instrumentID string, adj Adjustment) (Change, error) {
	cue, err := s.Cue(cueID)
	if err != nil {
		return Change{}, err
	}
	in, ok := LookupInstrument(instrumentID)
	if !ok {
		return Change{}, unknownInstrument(instrumentID)
	}

	current := cue.levels[in.ID]
	next, err := adj.Apply(current, MaxSendLevel)
	if err != nil {
		return Change{}, fmt.Errorf("cue %d %s: %w", cueID, in.ID, err)
	}
	if err := ValidateSend(next); err != nil {
		return Change{}, fmt.Errorf("cue %d %s: %w", cueID, in.ID, err)
	}

	cue.levels[in.ID] = next
	return Change{From: current, To: next}, nil
}

// SetMonitorVolume adjusts the control-room level.
func (s *Session) SetMonitorVolume(adj Adjustment) (Change, error) {
	current := s.monitor.Volume
	next, err := adj.Apply(current, MaxMonitorLevel)
	if err != nil {
		return Change{}, fmt.Errorf("monitor: %w", err)
	}
	if err := ValidateMonitor(next); err != nil {
		return Change{}, fmt.Errorf("monitor: %w", err)
	}
	s.monitor.Volume = next
	return Change{From: current, To: next}, nil
}

// SetMonitorMute mutes or unmutes the control room.
func (s *Session) SetMonitorMute(muted bool) { s.monitor.Muted = muted }

// SetEffect enables or disables one effect in an instrument's chain.
func (s *Session) SetEffect(instrumentID, effectID string, enabled bool) error {
	in, ok := LookupInstrument(instrumentID)
	if !ok {
		return unknownInstrument(instrumentID)
	}
	eff, ok := LookupEffect(in.ID, effectID)
	if !ok {
		available := EffectIDs(in.ID)
		if len(available) == 0 {
			return fmt.Errorf("%s has no effects", in.ID)
		}
		return fmt.Errorf("%s has no effect %q (available: %v)", in.ID, effectID, available)
	}
	if s.fx[in.ID] == nil {
		s.fx[in.ID] = make(map[string]bool)
	}
	s.fx[in.ID][eff.ID] = enabled
	return nil
}

// EffectEnabled reports whether an effect is on.
func (s *Session) EffectEnabled(instrumentID, effectID string) bool {
	in, ok := LookupInstrument(instrumentID)
	if !ok {
		return false
	}
	return s.fx[in.ID][normalizeID(effectID)]
}

// EnabledEffects lists the enabled effects for an instrument, in chain order.
func (s *Session) EnabledEffects(instrumentID string) []Effect {
	in, ok := LookupInstrument(instrumentID)
	if !ok {
		return nil
	}
	var out []Effect
	for _, e := range Chain(in.ID) {
		if s.fx[in.ID][e.ID] {
			out = append(out, e)
		}
	}
	return out
}

func unknownInstrument(id string) error {
	return fmt.Errorf("unknown instrument %q (available: %v)", id, InstrumentIDs())
}

// --- persistence ---

// file is the on-disk shape. It is separate from Session so the stored format
// stays a deliberate choice rather than a side effect of internal fields.
type file struct {
	Session struct {
		Name string `yaml:"name"`
	} `yaml:"session"`
	Monitor struct {
		Volume float64 `yaml:"volume"`
		Muted  bool    `yaml:"muted"`
	} `yaml:"monitor"`
	Cues map[int]map[string]float64 `yaml:"cues"`
	FX   map[string]map[string]bool `yaml:"fx,omitempty"`
}

// Marshal renders the session as YAML.
func (s *Session) Marshal() ([]byte, error) {
	var f file
	f.Session.Name = s.Name
	f.Monitor.Volume = float64(s.monitor.Volume)
	f.Monitor.Muted = s.monitor.Muted

	f.Cues = make(map[int]map[string]float64, len(s.cues))
	for id, cue := range s.cues {
		levels := make(map[string]float64, len(cue.levels))
		for inst, lvl := range cue.levels {
			levels[inst] = float64(lvl)
		}
		f.Cues[id] = levels
	}

	f.FX = make(map[string]map[string]bool, len(s.fx))
	for inst, effects := range s.fx {
		if len(effects) == 0 {
			continue
		}
		copied := make(map[string]bool, len(effects))
		for id, on := range effects {
			copied[id] = on
		}
		f.FX[inst] = copied
	}

	out, err := yaml.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encoding session: %w", err)
	}
	return out, nil
}

// Unmarshal reads a session from YAML.
//
// Stored values are validated rather than trusted: a hand-edited file is an
// expected way to work, and a typo there must not become a routing change.
// Anything the file does not mention keeps its default.
func Unmarshal(data []byte) (*Session, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	s := NewSession(f.Session.Name)
	if s.Name == "" {
		s.Name = "untitled"
	}

	monitor := Level(f.Monitor.Volume)
	if err := ValidateMonitor(monitor); err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}
	s.monitor = Monitor{Volume: monitor, Muted: f.Monitor.Muted}

	for cueID, levels := range f.Cues {
		cue, err := s.Cue(cueID)
		if err != nil {
			return nil, fmt.Errorf("reading session: %w", err)
		}
		for instID, raw := range levels {
			in, ok := LookupInstrument(instID)
			if !ok {
				return nil, fmt.Errorf("reading session: cue %d: %w", cueID, unknownInstrument(instID))
			}
			lvl := Level(raw)
			if err := ValidateSend(lvl); err != nil {
				return nil, fmt.Errorf("reading session: cue %d %s: %w", cueID, in.ID, err)
			}
			cue.levels[in.ID] = lvl
		}
	}

	for instID, effects := range f.FX {
		for effID, on := range effects {
			// An unknown effect is reported rather than dropped, so a rename
			// upstream surfaces instead of silently disabling processing.
			if err := s.SetEffect(instID, effID, on); err != nil {
				return nil, fmt.Errorf("reading session: %w", err)
			}
		}
	}
	return s, nil
}
