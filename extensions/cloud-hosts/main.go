// Command cloud-hosts is Persona's provider of rented servers (docs/extensions/cloud-hosts.md): it answers the
// cloud_hosts extension point (cloud.instances, cloud.instance, cloud.action, cloud.snapshots) for AWS (EC2 and
// Lightsail), Google Compute Engine, Oracle Cloud Infrastructure, the common VPS panels (Pterodactyl / Pelican,
// SolusVM 1 and 2, Virtualizor, Proxmox VE, Hetzner Cloud, DigitalOcean, Vultr) and servers without an API.
//
// The hub starts the program with no arguments and speaks the extension protocol to it. Every credential comes from
// the hub's sealed secrets with initialize and stays in this process: nothing is written to disk, logged or answered.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

var about = func() (m struct{ ID, Version string }) {
	if err := json.Unmarshal(manifest, &m); err != nil {
		panic(err)
	}
	return m
}()

const usage = `cloud-hosts: Persona's provider of cloud instances and hosted servers.

  cloud-hosts            the extension (started by the Persona hub; protocol on stdin / stdout)
  cloud-hosts manifest   print persona-extension.json
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "manifest":
			os.Stdout.Write(manifest)
			return
		default:
			fmt.Print(usage)
			return
		}
	}
	ext.Main(program())
}

// program is the extension: its providers are built from the configuration and the secrets the hub sends with
// initialize, and every cloud.* call goes to the provider its contribution names.
func program() ext.Program {
	a := &app{}
	return ext.Program{
		ID: about.ID, Version: about.Version, Manifest: manifest,
		Init: func(_ context.Context, in *ext.Init, _ *ext.Hub) (map[string]any, error) {
			return a.init(in)
		},
		Namespaces: map[string]ext.Handler{ext.CloudNamespace: a.handle},
	}
}
