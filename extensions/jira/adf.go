package main

import "strings"

// adfDocument turns plain text into an Atlassian Document Format document, the form Jira Cloud's REST v3 takes for a
// description and a comment body. Each line is a paragraph with one text node; a blank line is a paragraph without
// content, because a text node must not be empty. Nothing is parsed, so the text stays what the user wrote.
func adfDocument(text string) map[string]any {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	content := make([]any, 0, len(lines))
	for _, line := range lines {
		paragraph := map[string]any{"type": "paragraph"}
		if line != "" {
			paragraph["content"] = []any{map[string]any{"type": "text", "text": line}}
		}
		content = append(content, paragraph)
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}
