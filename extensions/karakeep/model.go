package main

import "encoding/json"

type bookmark struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Type        string          `json:"type"`
	URL         string          `json:"url"`
	Content     string          `json:"content"`
	Text        string          `json:"text"`
	Description string          `json:"description"`
	Note        string          `json:"note"`
	Summary     string          `json:"summary"`
	Favourited  bool            `json:"favourited"`
	Archived    bool            `json:"archived"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
	Tags        json.RawMessage `json:"tags"`
}

type bookmarkList struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	ParentID string `json:"parentId"`
}

type highlight struct {
	ID         string `json:"id"`
	BookmarkID string `json:"bookmarkId"`
	Text       string `json:"text"`
	Note       string `json:"note"`
	Color      string `json:"color"`
	CreatedAt  string `json:"createdAt"`
}

type page[T any] struct {
	Bookmarks  []T     `json:"bookmarks"`
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

type updatePatch struct {
	Note       *string `json:"note,omitempty"`
	Favourited *bool   `json:"favourited,omitempty"`
	Archived   *bool   `json:"archived,omitempty"`
}

func (p updatePatch) empty() bool { return p.Note == nil && p.Favourited == nil && p.Archived == nil }
