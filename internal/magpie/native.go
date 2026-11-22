package magpie

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

// Guest-native mode (docs/design.md section 4.4.2): each harness signs in
// itself inside the VM and sends model traffic directly to the provider.
// The host runs no magpie, keeps no subscription or API key, and routes no
// model traffic. The models routes then list native model IDs and refuse
// every gateway change.

// ModeGuestNative is GatewayInfo.Mode in guest-native mode.
const ModeGuestNative = "guest-native"

// StateOff is the gateway state in guest-native mode.
const StateOff = "off"

// ErrGuestNative is returned for a gateway change in guest-native mode.
var ErrGuestNative = fmt.Errorf("%w: sign-ins and API keys are set up in the VM in guest-native mode; the host model gateway is off", ErrConflict)

// NativeAliases are model IDs that a harness always accepts as --model.
// Claude Code documents these aliases; the other harnesses get their
// models from the sessions (NativeCatalog).
var NativeAliases = map[string][]string{
	"claude-code": {"opus", "sonnet", "haiku"},
}

var nativeModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+/\[\]-]*$`)

// ValidNativeModelID reports whether id can be a native model ID of a
// harness ("opus", "gpt-6.1", "openai/gpt-6").
func ValidNativeModelID(id string) bool { return len(id) <= 256 && nativeModelRe.MatchString(id) }

// NativeModel is a model that a harness used, for NativeCatalog.
type NativeModel struct {
	Harness string
	Model   string
}

// NativeCatalog returns the native model list: the aliases of each
// harness, then the models of used (sessions and bindings), one entry per
// harness and model. Model.Provider is the harness ID.
func NativeCatalog(used []NativeModel) []Model {
	out := []Model{}
	seen := map[NativeModel]bool{}
	add := func(h, m string) {
		k := NativeModel{Harness: h, Model: m}
		if m == "" || seen[k] || !ValidNativeModelID(m) {
			return
		}
		seen[k] = true
		out = append(out, Model{ID: m, Provider: h, Name: m})
	}
	harnesses := make([]string, 0, len(NativeAliases))
	for h := range NativeAliases {
		harnesses = append(harnesses, h)
	}
	slices.Sort(harnesses)
	for _, h := range harnesses {
		for _, m := range NativeAliases[h] {
			add(h, m)
		}
	}
	for _, u := range used {
		add(u.Harness, u.Model)
	}
	return out
}

// nativeModels returns the native catalog of the service.
func (s *Service) nativeModels(ctx context.Context) []Model {
	var used []NativeModel
	if s.NativeUsed != nil {
		used = s.NativeUsed(ctx)
	}
	return NativeCatalog(used)
}
