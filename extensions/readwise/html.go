package main

import (
	"errors"
	"strings"

	"golang.org/x/net/html"
)

// articleText turns an article into bounded reading text, never executable markup.
// Script/style/form content is excluded even if it contains instructions to the assistant.
func articleText(source string) (string, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", err
	}
	var blocks []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg", "form", "button", "nav", "footer", "iframe", "template":
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.Join(strings.Fields(n.Data), " ")
			if text != "" && len(blocks) < 300 {
				blocks = append(blocks, text)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && isBlock(n.Data) && len(blocks) > 0 {
			blocks = append(blocks, "\n")
		}
	}
	walk(root)
	var out strings.Builder
	line := strings.Builder{}
	for _, block := range blocks {
		if block == "\n" {
			if line.Len() > 0 {
				out.WriteString(line.String() + "\n\n")
				line.Reset()
			}
			continue
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(block)
	}
	if line.Len() > 0 {
		out.WriteString(line.String())
	}
	return clipText(strings.TrimSpace(out.String()), 120000), nil
}

func isBlock(tag string) bool {
	switch tag {
	case "article", "aside", "blockquote", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "main", "p", "pre", "section", "tr":
		return true
	}
	return false
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}

func validDocumentID(id string) error {
	if len(id) == 0 || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n/") {
		return errors.New("document ID is invalid")
	}
	return nil
}
