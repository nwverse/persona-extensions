// The WhatsApp connector as a Persona extension: native multi-device protocol over go.mau.fi/whatsmeow
// and pure-Go SQLite (modernc.org/sqlite, CGO_ENABLED=0). The hub launches this program and speaks the
// Persona extension process protocol over stdin/stdout via sdk/go/ext. Device credentials and Signal keys
// stay encrypted in the hub's connector_secrets store (never plaintext on disk).
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/serve"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa/spec"
	"github.com/nwverse/persona-extensions/pluginkit"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

var version = func() string {
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.Version
}()

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println(version)
			return
		default:
			fmt.Fprintln(os.Stderr, "usage: whatsapp [version]: started by the Persona hub, which talks to it over stdin / stdout")
			os.Exit(2)
		}
	}
	log.SetFlags(0) // the hub prefixes each log line it captures
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serve.MainWith(ctx, os.Stdin, os.Stdout, spec.ID, version, manifest, build)
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		log.Printf("stopped: %v", err)
		os.Exit(1)
	}
}

func build(init serve.Init, secrets connector.Secrets) (connector.Connector, error) {
	cfg := wa.ParseConfig(init.Config)
	return wa.New(context.Background(), secrets, init.DataDir, userZone(init.Timezone), cfg)
}

func userZone(name string) *time.Location {
	if name == "" {
		return nil
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("time zone %q: %v", name, err)
		return nil
	}
	return zone
}
