package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// initialPreferences is written into a brand-new profile's Default/Preferences
// so that features which generate non-user network traffic (and would
// correlate identities at Google endpoints) start disabled. Existing
// profiles are never modified. Each entry is documented in
// docs/chromium-flags.md.
var initialPreferences = map[string]any{
	"safebrowsing": map[string]any{
		"enabled":                 false,
		"enhanced":                false,
		"scout_reporting_enabled": false,
	},
	"signin":                map[string]any{"allowed": false, "allowed_on_next_startup": false},
	"search":                map[string]any{"suggest_enabled": false},
	"alternate_error_pages": map[string]any{"enabled": false},
	"dns_prefetching":       map[string]any{"enabled": false},
	"net": map[string]any{
		"network_prediction_options": 2, // never preconnect/prefetch
	},
	"translate":  map[string]any{"enabled": false},
	"spellcheck": map[string]any{"use_spelling_service": false},
	"browser": map[string]any{
		"show_home_button":                false,
		"has_seen_welcome_page":           true,
		"check_default_browser":           false,
		"default_browser_setting_enabled": false,
	},
	"profile": map[string]any{
		"password_manager_enabled": false,
		"default_content_setting_values": map[string]any{
			"notifications": 2, // block
			"geolocation":   2,
		},
	},
	"credentials_enable_service":    false,
	"credentials_enable_autosignin": false,
	"autofill":                      map[string]any{"profile_enabled": false, "credit_card_enabled": false},
	"webrtc": map[string]any{
		"ip_handling_policy":      "disable_non_proxied_udp",
		"multiple_routes_enabled": false,
		"nonproxied_udp_enabled":  false,
	},
	"url_keyed_anonymized_data_collection": map[string]any{"enabled": false},
	"optimization_guide":                   map[string]any{"fetching_enabled": false},
	"privacy_sandbox": map[string]any{
		"m1": map[string]any{
			"topics_enabled":         false,
			"fledge_enabled":         false,
			"ad_measurement_enabled": false,
		},
	},
	"default_apps_install_state": 3,
}

// SeedPreferences writes the privacy preference set into a fresh profile.
// It is a no-op when the profile already has preferences.
func SeedPreferences(profileDir string) error {
	def := filepath.Join(profileDir, "Default")
	prefs := filepath.Join(def, "Preferences")
	if _, err := os.Stat(prefs); err == nil {
		return nil
	}
	if err := os.MkdirAll(def, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(initialPreferences, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(prefs, b, 0o600)
}
