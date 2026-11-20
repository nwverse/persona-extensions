package main

import (
	"errors"
	"strings"
)

// checkADF accepts what Jira Cloud's REST v3 documents for a description or a comment body: an Atlassian Document
// Format document, {"type":"doc","version":1,"content":[...]}, in which a text node holds a non-empty string. A plain
// string is refused. The structure follows Atlassian's ADF pages for the doc, paragraph and text nodes.
func checkADF(value any) error {
	doc, ok := value.(map[string]any)
	if !ok {
		return errors.New("not an Atlassian Document")
	}
	if doc["type"] != "doc" || doc["version"] != float64(1) {
		return errors.New("a document needs type doc and version 1")
	}
	content, ok := doc["content"].([]any)
	if !ok {
		return errors.New("a document needs content")
	}
	return checkADFNodes(content)
}

func checkADFNodes(nodes []any) error {
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			return errors.New("a node must be an object")
		}
		switch node["type"] {
		case "text":
			if text, _ := node["text"].(string); text == "" {
				return errors.New("a text node must not be empty")
			}
		case "paragraph":
			if children, present := node["content"]; present {
				list, ok := children.([]any)
				if !ok {
					return errors.New("paragraph content must be an array")
				}
				if err := checkADFNodes(list); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected node type")
		}
	}
	return nil
}

// adfText reads a checked document back as the plain text it was made from: one line per paragraph.
func adfText(value any) string {
	doc, _ := value.(map[string]any)
	content, _ := doc["content"].([]any)
	lines := make([]string, 0, len(content))
	for _, raw := range content {
		node, _ := raw.(map[string]any)
		children, _ := node["content"].([]any)
		var line strings.Builder
		for _, child := range children {
			text, _ := child.(map[string]any)["text"].(string)
			line.WriteString(text)
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}
