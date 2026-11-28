// Package spec declares the WhatsApp connector's static identity, capabilities, approval rules, and inbox kinds.
// plugin_test.go asserts that persona-extension.json matches this manifest verbatim.
package spec

import (
	"slices"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/prompts"
)

const (
	ID   = "whatsapp"
	Name = "WhatsApp"
)

// ReadTools are the assistant-facing read-only MCP tools (including on-demand local history backfill).
var ReadTools = []string{
	"whatsapp_list_chats",
	"whatsapp_read_messages",
	"whatsapp_search_messages",
	"whatsapp_get_message",
	"whatsapp_list_contacts",
	"whatsapp_history_backfill",
}

// WriteActions are the write MCP tools; every one is listed in ConfirmAlways so the hub always requires
// phone confirmation before execution.
var WriteActions = []string{
	"whatsapp_send",
	"whatsapp_react",
	"whatsapp_mark_read",
}

// Manifest returns the connector manifest declared at initialize time.
func Manifest() connector.Manifest {
	return connector.Manifest{
		Capabilities: []connector.Capability{
			connector.CapStatus,
			connector.CapTools,
			connector.CapCatalog,
			connector.CapActions,
			connector.CapEvents,
			connector.CapInbox,
			connector.CapArchive,
			connector.CapLogin,
			connector.CapThreads,
			connector.CapFallback,
		},
		ConfirmAlways:    slices.Clone(WriteActions),
		Kinds:            []string{"dm", "mention"},
		KindDescriptions: prompts.KindDescriptions,
	}
}
