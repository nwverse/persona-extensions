// Orca bot hands new Persona to-dos to a bot of the Orca desktop app, and lets that bot write its preparation back.
// One binary, three roles:
//
//	orca-bot hub    the hub program: hears new to-dos (todo.changed), filters them, asks the node program to hand
//	                each one to the chosen bot, notifies the owner when the bot finished, and writes the bot's
//	                write-backs onto the to-do. It never talks to Orca itself.
//	orca-bot node   the program on the one Mac the owner picked: speaks Orca's local runtime socket (bots.list,
//	                bots.session.start, agentSession.send, agentSession.history), and serves the MCP command below
//	                over a private unix socket.
//	orca-bot mcp    a stdio MCP server the owner registers as one of the bot's own tools (orca bot tools add); it
//	                reaches only the node program's socket, and through it only the to-dos handed to that bot.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var authorManifest []byte

// manifest is the manifest as packaged: without the source-only tags, replaces and icon, which older hubs refuse.
var manifest = packagedManifest(authorManifest)

func packagedManifest(raw []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic("invalid embedded extension manifest")
	}
	delete(fields, "tags")
	delete(fields, "replaces")
	delete(fields, "icon")
	out, err := json.Marshal(fields)
	if err != nil {
		panic("cannot encode embedded extension manifest")
	}
	return out
}

var about = func() (out struct{ ID, Version string }) {
	if err := json.Unmarshal(manifest, &out); err != nil {
		panic(err)
	}
	return out
}()

func main() {
	log.SetFlags(0)
	role := ""
	if len(os.Args) > 1 {
		role = os.Args[1]
	}
	switch role {
	case "hub", "manifest":
		ext.Main(newHubApp(nil, nil).program())
	case "node":
		ext.Main(newNodeApp(nil).program())
	case "mcp":
		os.Exit(runMCP(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	default:
		fmt.Fprintln(os.Stderr, "usage: orca-bot hub | node | mcp --socket <path> --bot <id>   (hub and node are started by Persona)")
		os.Exit(2)
	}
}
