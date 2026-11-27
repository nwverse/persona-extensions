// Command steam provides read-only Steam data to Persona.
package main

import (
	_ "embed"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
)

const (
	extensionID = "steam"
	version     = "0.1.0"
	summaryID   = "steam-summary"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

func main() {
	ext.Main(ext.Program{ID: extensionID, Version: version, Manifest: manifest})
}
