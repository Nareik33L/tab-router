//go:build !linux

package browser

// PlatformFlags is empty on product platforms (Windows, macOS).
func PlatformFlags() []string { return nil }
