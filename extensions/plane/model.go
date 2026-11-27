package main

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

type project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Identifier  string `json:"identifier"`
	Description string `json:"description"`
}

type workItem struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	DescriptionStripped string          `json:"description_stripped"`
	DescriptionHTML     string          `json:"description_html"`
	SequenceID          int64           `json:"sequence_id"`
	Priority            string          `json:"priority"`
	Project             string          `json:"project"`
	ProjectIdentifier   string          `json:"project__identifier"`
	State               entityRef       `json:"state"`
	TargetDate          string          `json:"target_date"`
	WebURL              string          `json:"web_url"`
	UpdatedAt           string          `json:"updated_at"`
	Assignees           json.RawMessage `json:"assignees"`
}

type entityRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

func (r *entityRef) UnmarshalJSON(data []byte) error {
	var id string
	if json.Unmarshal(data, &id) == nil {
		r.ID = id
		return nil
	}
	type alias entityRef
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*r = entityRef(v)
	return nil
}

type workflowState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Sequence int    `json:"sequence"`
}

type page[T any] struct {
	Results         []T    `json:"results"`
	NextCursor      string `json:"next_cursor"`
	NextPageResults bool   `json:"next_page_results"`
	TotalResults    int    `json:"total_results"`
}

var (
	htmlBreak  = regexp.MustCompile(`(?i)</(p|div|li|h[1-6]|blockquote|pre)>|<br\s*/?>`)
	htmlTag    = regexp.MustCompile(`<[^>]*>`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// plainFromHTML is the text of a description_html: paragraphs and breaks become line breaks, other tags go.
func plainFromHTML(markup string) string {
	text := html.UnescapeString(htmlTag.ReplaceAllString(htmlBreak.ReplaceAllString(markup, "\n"), ""))
	return strings.TrimSpace(blankLines.ReplaceAllString(text, "\n\n"))
}

// htmlFromPlain is a plain-text description as description_html, one paragraph per line.
func htmlFromPlain(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		b.WriteString("<p>" + html.EscapeString(line) + "</p>")
	}
	return b.String()
}

// itemDescription is the description as text. Plane's work item API answers description_html; the other two fields are
// read when an answer has them.
func itemDescription(item workItem) string {
	if text := strings.TrimSpace(item.DescriptionStripped); text != "" {
		return text
	}
	if text := plainFromHTML(item.DescriptionHTML); text != "" {
		return text
	}
	return item.Description
}
