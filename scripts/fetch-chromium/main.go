// Command fetch-chromium downloads the pinned Chromium build for this
// platform (or --platform), verifies its SHA-256 against
// browser/chromium/pin.json and extracts it under <data-dir>/chromium/<version>/
// where tab-router looks for it.
//
//	go run ./scripts/fetch-chromium                 # into the default data dir
//	go run ./scripts/fetch-chromium --dest dist/chromium --platform win64
//
// tab-router itself also fetches Chromium on first run if none is present.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nareik33L/tab-router/browser/chromium"
	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/routing/platform"
)

func main() {
	dest := flag.String("dest", "", "destination directory (default: <data-dir>/chromium)")
	plat := flag.String("platform", browser.PlatformKey(), "pin.json platform key")
	flag.Parse()
	if *dest == "" {
		d, err := platform.Current().DefaultDataDir()
		if err != nil {
			fail("%v", err)
		}
		*dest = filepath.Join(d, "chromium")
	}
	if _, err := chromium.Install(*dest, *plat, os.Stdout); err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fetch-chromium: "+format+"\n", args...)
	os.Exit(1)
}
