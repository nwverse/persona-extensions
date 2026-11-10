package main

import (
	"strings"

	"golang.org/x/net/html"
)

var droppedElements = map[string]bool{"script": true, "style": true, "iframe": true, "object": true, "embed": true, "form": true, "template": true, "svg": true, "math": true, "noscript": true}

func plainText(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var walk func(*html.Node) string
	walk = func(n *html.Node) string {
		if n.Type == html.ElementNode && droppedElements[n.Data] {
			return ""
		}
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			b.WriteString(walk(child))
		}
		return b.String()
	}
	return strings.Join(strings.Fields(walk(doc)), " ")
}
