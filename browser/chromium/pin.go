// Package chromium embeds the pinned Chromium build manifest so the binary
// always knows which build it was released with.
package chromium

import (
	_ "embed"
	"encoding/json"

	"github.com/Nareik33L/tab-router/controller/browser"
)

//go:embed pin.json
var pinJSON []byte

// Pin returns the embedded manifest.
func Pin() *browser.PinManifest {
	var m browser.PinManifest
	if err := json.Unmarshal(pinJSON, &m); err != nil {
		return nil
	}
	return &m
}
