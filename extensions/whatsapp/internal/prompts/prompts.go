// Package prompts embeds the WhatsApp connector's MCP tool descriptions and JSON input schemas
// (mcp/<tool>.md: description, blank line, then ```json schema block) and its inbox kind descriptions.
package prompts

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed mcp/*.md
var files embed.FS

// MCPTool is one MCP tool definition exposed to the assistant and subscriptions catalog.
type MCPTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // compact JSON
}

var mcpTools = loadMCP()

const schemaFence = "\n\n```json\n"

func loadMCP() map[string]MCPTool {
	out := map[string]MCPTool{}
	err := fs.WalkDir(files, "mcp", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		desc, rest, ok := strings.Cut(text, schemaFence)
		schema, ok2 := strings.CutSuffix(rest, "\n```\n")
		if !ok2 {
			schema, ok2 = strings.CutSuffix(rest, "\n```")
		}
		if !ok || !ok2 || desc == "" {
			return fmt.Errorf("%s: want the description, an empty line and a ```json block with the input schema", p)
		}
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(schema)); err != nil {
			return fmt.Errorf("%s: input schema: %w", p, err)
		}
		name := strings.TrimSuffix(path.Base(p), ".md")
		out[name] = MCPTool{Name: name, Description: desc, InputSchema: b.Bytes()}
		return nil
	})
	if err != nil {
		panic("prompts: " + err.Error())
	}
	return out
}

// Tool returns the MCP tool named name; an unknown name is a bug and panics.
func Tool(name string) MCPTool {
	t, ok := mcpTools[name]
	if !ok {
		panic("prompts: no MCP tool " + name)
	}
	return t
}

// ToolNames lists every tool embedded here.
func ToolNames() []string {
	out := make([]string, 0, len(mcpTools))
	for n := range mcpTools {
		out = append(out, n)
	}
	return out
}

// KindDescriptions tells the inbox triage model what each WhatsApp inbox kind means.
var KindDescriptions = map[string]string{
	"dm":      "WhatsApp 里有人私聊用户，还没读",
	"mention": "WhatsApp 群里 @ 了用户，或回复了用户的消息",
}
