// Command steam provides read-only Steam data to Persona.
package main

import (
	"context"
	_ "embed"
	"net/http"
	"os"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
)

const (
	extensionID = "steam"
	version     = "0.2.0"
	summaryID   = "steam-summary"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

type app struct {
	*steamData
	qr     *qrAuth
	locale string
}

func main() {
	ext.Main(program(nil, time.Now))
}

func program(client *http.Client, now func() time.Time) ext.Program {
	return (&app{}).program(client, now)
}

func (a *app) program(client *http.Client, now func() time.Time) ext.Program {
	if client == nil {
		client = steamHTTP(nil, httpTimeout)
	} else {
		client = steamHTTP(client.Transport, client.Timeout)
	}
	return ext.Program{
		ID: extensionID, Version: version, Manifest: manifest,
		Init: func(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
			var cfg config
			if in.Config(&cfg) != nil {
				return nil, invalid("Invalid Steam configuration.")
			}
			dir := os.Getenv("PERSONA_EXTENSION_DATA_DIR")
			if dir == "" {
				dir = in.Info().DataDir
			}
			if dir == "" {
				dir = os.Getenv("PERSONA_EXTENSION_DATA")
			}
			a.steamData = newSteamData(cfg, in.Secrets[keyName], dir, client, now)
			a.locale = in.Hub.Locale
			if a.config.AuthMode == modeQR {
				a.qr = newQRAuth(ctx, hub, client, now, a.config)
			}
			return nil, nil
		},
		Namespaces: map[string]ext.Handler{"connector": a.connector, "auth": ext.AuthHandler(a)},
		UI: map[string]ext.Page{
			extensionID: {Render: a.render, Action: a.action},
			summaryID:   {Render: a.card},
		},
	}
}
