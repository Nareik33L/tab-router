//go:build !darwin

package tor

func prepareExecutable(string) error { return nil }
