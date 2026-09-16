package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ETLopes/cli/internal/audio"
	"github.com/ETLopes/cli/internal/config"
	"github.com/ETLopes/cli/internal/i18n"
	"github.com/ETLopes/cli/internal/separate"
)

// The settings registry describes what can be configured and how each value
// should be chosen. Naming the shape of a setting -- a fixed set of options, a
// number, free text -- lets the editor offer the right thing rather than
// making everyone type an exact string and hope.

// settingKind is how a value is picked.
type settingKind int

const (
	// kindChoice has a fixed set of valid values, cycled in place.
	kindChoice settingKind = iota
	// kindText is free text, typed.
	kindText
	// kindNumber is typed, but only digits are accepted.
	kindNumber
	// kindToggle is on or off.
	kindToggle
	// kindList is a comma-separated set, typed.
	kindList
)

// choice is one option for a kindChoice setting.
type choice struct {
	Value string
	Label string
}

// setting is one configurable value.
type setting struct {
	Key     string
	Label   string
	Kind    settingKind
	Choices []choice
	// Help explains what the setting does, shown beneath the list.
	Help string
}

// group is a section of related settings.
type group struct {
	Title    string
	Settings []setting
}

// settingGroups describes everything configurable, in the order it is shown.
func settingGroups() []group {
	langChoices := make([]choice, 0, len(i18n.Supported()))
	for _, l := range i18n.Supported() {
		langChoices = append(langChoices, choice{Value: string(l), Label: l.Name()})
	}

	modelChoices := make([]choice, 0, len(config.KnownModels))
	for _, m := range config.KnownModels {
		label := m
		switch m {
		case separate.ModelDefault:
			label = m + " — " + i18n.T("set.model.standard")
		case separate.ModelFineTuned:
			label = m + " — " + i18n.T("set.model.finetuned")
		case separate.ModelSixStem:
			label = m + " — " + i18n.T("set.model.sixstem")
		}
		modelChoices = append(modelChoices, choice{Value: m, Label: label})
	}

	return []group{
		{
			Title: i18n.T("set.group.general"),
			Settings: []setting{
				{Key: config.KeyLang, Label: i18n.T("set.lang"), Kind: kindChoice,
					Choices: langChoices, Help: i18n.T("set.lang.help")},
			},
		},
		{
			Title: i18n.T("set.group.studio"),
			Settings: []setting{
				{Key: config.KeyReaperHost, Label: i18n.T("set.reaper_host"), Kind: kindText,
					Help: i18n.T("set.reaper_host.help")},
				{Key: config.KeyReaperPort, Label: i18n.T("set.reaper_port"), Kind: kindNumber,
					Help: i18n.T("set.reaper_port.help")},
				{Key: config.KeySession, Label: i18n.T("set.session"), Kind: kindText,
					Help: i18n.T("set.session.help")},
				{Key: config.KeySessionDir, Label: i18n.T("set.session_dir"), Kind: kindText,
					Help: i18n.T("set.session_dir.help")},
			},
		},
		{
			Title: i18n.T("set.group.dtx"),
			Settings: []setting{
				{Key: config.KeyModel, Label: i18n.T("set.model"), Kind: kindChoice,
					Choices: modelChoices, Help: i18n.T("set.model.help")},
				{Key: config.KeyDevice, Label: i18n.T("set.device"), Kind: kindChoice,
					Choices: []choice{
						{Value: separate.DeviceAuto, Label: i18n.T("set.device.auto")},
						{Value: separate.DeviceCPU, Label: "cpu"},
						{Value: separate.DeviceMPS, Label: "mps"},
						{Value: separate.DeviceCUDA, Label: "cuda"},
					}, Help: i18n.T("set.device.help")},
				{Key: config.KeyFormats, Label: i18n.T("set.formats"), Kind: kindList,
					Help: i18n.Tf("set.formats.help", strings.Join(audio.EncodingIDs(), ", "))},
				{Key: config.KeyOutputDir, Label: i18n.T("set.output_dir"), Kind: kindText,
					Help: i18n.T("set.output_dir.help")},
				{Key: config.KeyNormalize, Label: i18n.T("set.normalize"), Kind: kindToggle,
					Help: i18n.T("set.normalize.help")},
				{Key: config.KeyLimit, Label: i18n.T("set.limit"), Kind: kindToggle,
					Help: i18n.T("set.limit.help")},
				{Key: config.KeyUSBPath, Label: i18n.T("set.usb_path"), Kind: kindText,
					Help: i18n.T("set.usb_path.help")},
			},
		},
	}
}

// flatSettings returns every setting with its group, in display order.
func flatSettings() ([]setting, []string) {
	var out []setting
	var headings []string
	for _, g := range settingGroups() {
		for i, s := range g.Settings {
			out = append(out, s)
			if i == 0 {
				headings = append(headings, g.Title)
			} else {
				headings = append(headings, "")
			}
		}
	}
	return out, headings
}

// nextChoice cycles a fixed-set value, wrapping at the end. An unrecognised
// current value lands on the first option rather than sticking.
func nextChoice(s setting, current string, step int) string {
	if len(s.Choices) == 0 {
		return current
	}
	idx := 0
	for i, c := range s.Choices {
		if c.Value == current {
			idx = i
			break
		}
	}
	idx = (idx + step + len(s.Choices)) % len(s.Choices)
	return s.Choices[idx].Value
}

// displayValue renders a value for the list, naming what an empty one means
// rather than leaving a blank the reader has to interpret.
func displayValue(s setting, raw string) string {
	switch s.Kind {
	case kindChoice:
		for _, c := range s.Choices {
			if c.Value == raw {
				return c.Label
			}
		}
	case kindToggle:
		if raw == "true" {
			return i18n.T("set.on")
		}
		return i18n.T("set.off")
	}
	if strings.TrimSpace(raw) == "" {
		return i18n.T("set.unset")
	}
	return raw
}

// acceptsRune reports whether a typed character belongs in this value, which
// is what stops a letter reaching a port number.
func (s setting) acceptsRune(r rune) bool {
	if r < 32 || r == 127 {
		return false
	}
	if s.Kind == kindNumber {
		return r >= '0' && r <= '9'
	}
	return true
}

// validate checks a value using the same rules the command line applies, so
// the two cannot disagree about what is acceptable.
func (s setting) validate(value string) error {
	if err := validateSetting(s.Key, value); err != nil {
		return err
	}
	if s.Kind == kindNumber && strings.TrimSpace(value) != "" {
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%s: %s", s.Label, i18n.T("set.err.number"))
		}
	}
	if s.Kind == kindList {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := audio.LookupEncoding(part); !ok {
				return fmt.Errorf("%s: %q — %s", s.Label, part,
					strings.Join(audio.EncodingIDs(), ", "))
			}
		}
	}
	return nil
}
