package tor

// Prepare makes the tor binary and its bundled libraries runnable on this
// OS (ad-hoc codesign on macOS). It is safe to call concurrently and
// repeatedly; a bundle that already verifies is left untouched.
func Prepare(bin string) error {
	return prepareExecutable(bin)
}
