package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Environment is the device-level configuration an identity runs with. It
// is chosen once, when the identity is created, stored in identity.json and
// applied identically on every launch. Only settings Chromium legitimately
// exposes are included; nothing here spoofs or randomises fingerprints.
type Environment struct {
	// Locale is the Chromium UI/Intl locale, e.g. "en-US".
	Locale string `json:"locale"`
	// AcceptLanguages is the Accept-Language preference, e.g. "en-US,en".
	AcceptLanguages string `json:"accept_languages"`
	// Timezone is an IANA zone applied to the process (TZ) and enforced via
	// DevTools on every page, e.g. "Europe/London".
	Timezone string `json:"timezone"`
	// Window geometry in CSS pixels.
	WindowWidth  int `json:"window_width"`
	WindowHeight int `json:"window_height"`
	WindowX      int `json:"window_x"`
	WindowY      int `json:"window_y"`
	// DeviceScaleFactor forces a stable DPI regardless of the host display.
	DeviceScaleFactor float64 `json:"device_scale_factor"`
	// ColorScheme is "light" or "dark".
	ColorScheme string `json:"color_scheme"`
	// DownloadDir is relative to the identity directory.
	DownloadDir string `json:"download_dir"`
}

// EnvironmentDefaults are the values used for identities created without
// explicit configuration. Empty Locale/Timezone mean "detect from host at
// creation time" (then pinned).
type EnvironmentDefaults struct {
	Locale            string
	Timezone          string
	WindowWidth       int
	WindowHeight      int
	DeviceScaleFactor float64
	ColorScheme       string
}

var localeRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// Validate checks the environment is well-formed.
func (e Environment) Validate() error {
	if !localeRe.MatchString(e.Locale) {
		return fmt.Errorf("environment: invalid locale %q (expected e.g. en-US)", e.Locale)
	}
	if e.Timezone == "" {
		return fmt.Errorf("environment: timezone is required")
	}
	if _, err := time.LoadLocation(e.Timezone); err != nil {
		return fmt.Errorf("environment: unknown timezone %q", e.Timezone)
	}
	if e.WindowWidth < 320 || e.WindowHeight < 240 {
		return fmt.Errorf("environment: window %dx%d too small", e.WindowWidth, e.WindowHeight)
	}
	if e.DeviceScaleFactor < 0.5 || e.DeviceScaleFactor > 4 {
		return fmt.Errorf("environment: device_scale_factor %v out of range", e.DeviceScaleFactor)
	}
	if e.ColorScheme != "light" && e.ColorScheme != "dark" {
		return fmt.Errorf("environment: color_scheme must be light or dark")
	}
	if e.DownloadDir == "" || filepath.IsAbs(e.DownloadDir) || strings.Contains(e.DownloadDir, "..") {
		return fmt.Errorf("environment: download_dir must be a relative path inside the identity")
	}
	return nil
}

// NewEnvironment builds the pinned environment for identity index using the
// supplied defaults, filling detected host values where defaults are empty.
// Windows are tiled deterministically so identities never overlap exactly.
func NewEnvironment(index int, d EnvironmentDefaults) Environment {
	e := Environment{
		Locale:            d.Locale,
		Timezone:          d.Timezone,
		WindowWidth:       d.WindowWidth,
		WindowHeight:      d.WindowHeight,
		DeviceScaleFactor: d.DeviceScaleFactor,
		ColorScheme:       d.ColorScheme,
		DownloadDir:       "downloads",
	}
	if e.Locale == "" {
		e.Locale = detectLocale()
	}
	if e.Timezone == "" {
		e.Timezone = detectTimezone()
	}
	if e.WindowWidth == 0 || e.WindowHeight == 0 {
		e.WindowWidth, e.WindowHeight = 1280, 860
	}
	if e.DeviceScaleFactor == 0 {
		e.DeviceScaleFactor = 1
	}
	if e.ColorScheme == "" {
		e.ColorScheme = "light"
	}
	e.AcceptLanguages = acceptLanguagesFor(e.Locale)
	// Deterministic tiling: identity 1 at the origin, each subsequent window
	// stepped diagonally so title bars stay visible.
	e.WindowX = 40 + (index-1)*60
	e.WindowY = 40 + (index-1)*60
	return e
}

func acceptLanguagesFor(locale string) string {
	base, _, hasRegion := strings.Cut(locale, "-")
	if hasRegion {
		return locale + "," + base
	}
	return locale
}

func detectLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := os.Getenv(k)
		if v == "" || v == "C" || v == "POSIX" {
			continue
		}
		v, _, _ = strings.Cut(v, ".")
		v = strings.ReplaceAll(v, "_", "-")
		if localeRe.MatchString(v) {
			return v
		}
	}
	return "en-US"
}

func detectTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink("/etc/localtime"); err == nil {
			if i := strings.Index(target, "zoneinfo/"); i >= 0 {
				tz := target[i+len("zoneinfo/"):]
				if _, err := time.LoadLocation(tz); err == nil {
					return tz
				}
			}
		}
	}
	if name := time.Now().Location().String(); name != "" && name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}
