package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ewolf/dogit/internal/models"
)

// missingRequiredSettings lists the settings a module says it cannot work without and has not
// been given.
//
// Checked at registration rather than at first use, and that is the only place it can be
// checked usefully. A module that cannot reach its database still starts, still answers, and
// still deploys — it just records nothing, so from the outside it is indistinguishable from a
// module that has never deployed anything, and there is nothing on any page that says
// otherwise. The alternative is a module that is either working or absent, and an
// administrator installing it who is told why.
//
// The module's own manifest is the authority on what it needs, and the core reads the key list
// and nothing else: which of them are credentials is already decided by the secret flag, and
// nothing here opens a value or decides what one ought to be.
func (s *Server) missingRequiredSettings(ctx context.Context, integration *models.Integration) []string {
	settings, err := s.store.Integrations().SettingsFor(ctx, integration.ID, nil, nil, integration.Capabilities.Settings)
	if err != nil {
		// Reported as everything missing rather than nothing. A read that failed must not be
		// mistaken for a module that has filled everything in, and the two produce opposite
		// mistakes: one registers broken, the other refuses a module that is fine.
		s.log.Warn("a module's settings could not be read, so it is treated as having none",
			"kind", integration.Kind, "error", err)
		settings = map[string]json.RawMessage{}
	}

	missing := []string{}
	for _, spec := range integration.Capabilities.Settings {
		if !spec.Required {
			continue
		}
		if !settingHasValue(settings[spec.Key], spec) {
			missing = append(missing, spec.Key)
		}
	}
	return missing
}

// settingHasValue says whether a stored setting carries something.
//
// A default counts, because a module that ships with the right value for a setting is a module
// that has it. Nothing, an empty string and an explicit null do not: those are the three shapes
// a cleared field takes on the way here, and treating any of them as a value would let a module
// register with a setting nobody filled in — which is the one outcome this function exists to
// prevent, and one that looks like the check simply not being switched on.
//
// The absent case is the awkward one and it has caught this out before: reading a key that is
// not in the map gives the zero value of json.RawMessage, which is a nil byte slice inside a
// non-nil interface. Compared against nil it is not nil, it is merely empty — and anything that
// asks "is this nil?" about a value read out of a map has to ask about its length instead.
func settingHasValue(raw json.RawMessage, spec models.SettingSpec) bool {
	if len(raw) == 0 {
		return spec.Default != nil
	}
	// A stored setting is JSON, and the common case is a string. Only a string can be empty
	// without being absent, so only a string is worth looking at; anything else — a number, a
	// bool, a list — is something somebody put there on purpose.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text) != "" || spec.Default != nil
	}
	return true
}

// pluralSettings names a count of settings in the words the sentence around them needs.
func pluralSettings(count int) string {
	switch count {
	case 1:
		return "a setting it named"
	default:
		return fmt.Sprintf("%d settings it named", count)
	}
}
