package main

import "encoding/json"

// document uses the public Reader v3 representation. IDs are opaque strings.
type document struct {
	ID              string          `json:"id"`
	URL             string          `json:"url"`
	SourceURL       string          `json:"source_url"`
	Title           string          `json:"title"`
	Author          string          `json:"author"`
	Category        string          `json:"category"`
	Location        string          `json:"location"`
	SiteName        string          `json:"site_name"`
	WordCount       int             `json:"word_count"`
	ReadingTime     string          `json:"reading_time"`
	Summary         string          `json:"summary"`
	Content         string          `json:"content"`
	Notes           string          `json:"notes"`
	HTMLContent     string          `json:"html_content"`
	ParentID        string          `json:"parent_id"`
	ReadingProgress float64         `json:"reading_progress"`
	SavedAt         string          `json:"saved_at"`
	LastMovedAt     string          `json:"last_moved_at"`
	Tags            json.RawMessage `json:"tags"`
}

type listPage struct {
	Count   int        `json:"count"`
	Cursor  string     `json:"nextPageCursor"`
	Results []document `json:"results"`
}

type listParams struct {
	ID       string
	Location string
	Category string
	HTML     bool
	Cursor   string
}

type updatePatch struct {
	Location        *string  `json:"location,omitempty"`
	ReadingProgress *float64 `json:"reading_progress,omitempty"`
	Notes           *string  `json:"notes,omitempty"`
}

func (p updatePatch) empty() bool {
	return p.Location == nil && p.ReadingProgress == nil && p.Notes == nil
}
