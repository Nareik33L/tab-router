//go:build !darwin

package chromium

func prepareExecutable(string) error { return nil }
