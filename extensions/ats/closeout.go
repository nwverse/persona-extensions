package main

import (
	"encoding/xml"
	"regexp"
	"strings"
)

var closeoutTag = regexp.MustCompile(`(?i)</closeout\s*>|<closeout(?:\s+(?:[^<>"']|"[^<"]*"|'[^<']*')*)?>`)

// parseCloseoutStatus 读取最后一段闭合的 closeout。标签属性用 XML 解析，正文按自由文本读取。
func parseCloseoutStatus(reply string) (status, waiting, missing string, ok bool) {
	text := closeoutProse(reply)
	var opening, lastOpening, body string
	var bodyStart int
	for _, bounds := range closeoutTag.FindAllStringIndex(text, -1) {
		tag := text[bounds[0]:bounds[1]]
		if strings.HasPrefix(tag, "</") {
			if opening != "" {
				lastOpening, body = opening, text[bodyStart:bounds[0]]
				opening = ""
			}
		} else if !strings.HasSuffix(tag, "/>") {
			opening, bodyStart = tag, bounds[1]
		}
	}
	if lastOpening == "" {
		return "", "", "", false
	}
	token, err := xml.NewDecoder(strings.NewReader(lastOpening)).Token()
	start, isStart := token.(xml.StartElement)
	if err != nil || !isStart {
		return "", "", "", false
	}
	for _, attr := range start.Attr {
		if attr.Name.Space == "" && strings.EqualFold(attr.Name.Local, "status") {
			status = strings.ToLower(strings.TrimSpace(attr.Value))
			if status == "not-done" {
				status = "not_done"
			}
			break
		}
	}
	for line := range strings.SplitSeq(body, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(value) {
		case "", "none", "n/a", "无":
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "waiting":
			waiting = value
		case "missing":
			missing = value
		}
	}
	return status, waiting, missing, true
}

func closeoutProse(reply string) string {
	var text strings.Builder
	var fence string
	for line := range strings.SplitAfterSeq(reply, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.TrimLeft(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			rest := strings.TrimLeft(trimmed, trimmed[:1])
			if trimmed[0] != '`' || !strings.ContainsRune(rest, '`') {
				fence = trimmed[:len(trimmed)-len(rest)]
				continue
			}
		}
		if !strings.HasPrefix(trimmed, ">") {
			text.WriteString(line)
		}
	}
	return text.String()
}
