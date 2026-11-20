package main

import "encoding/json"

type link struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	Description string          `json:"description"`
	Note        string          `json:"note"`
	Type        string          `json:"type"`
	Collection  *collection     `json:"collection"`
	Tags        json.RawMessage `json:"tags"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
}

type collection struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ParentID    *int64 `json:"parentId"`
}

type highlight struct {
	ID     int64  `json:"id"`
	LinkID int64  `json:"linkId"`
	Text   string `json:"text"`
	Note   string `json:"comment"`
	Color  string `json:"color"`
	Start  int    `json:"startOffset"`
	End    int    `json:"endOffset"`
}

type searchPage struct {
	Links      []link  `json:"links"`
	Data       []link  `json:"data"`
	NextCursor *string `json:"nextCursor"`
}
