package main

import "encoding/json"

type notionPage struct {
	ID             string                     `json:"id"`
	Object         string                     `json:"object"`
	URL            string                     `json:"url"`
	LastEditedTime string                     `json:"last_edited_time"`
	Properties     map[string]json.RawMessage `json:"properties"`
}

type notionList[T any] struct {
	Results    []T     `json:"results"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

type block struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	HasChildren bool      `json:"has_children"`
	Paragraph   richBlock `json:"paragraph"`
	Heading1    richBlock `json:"heading_1"`
	Heading2    richBlock `json:"heading_2"`
	Heading3    richBlock `json:"heading_3"`
	Bulleted    richBlock `json:"bulleted_list_item"`
	Numbered    richBlock `json:"numbered_list_item"`
	Todo        todoBlock `json:"to_do"`
	Quote       richBlock `json:"quote"`
	Callout     richBlock `json:"callout"`
	Toggle      richBlock `json:"toggle"`
	Code        richBlock `json:"code"`
	ChildPage   richBlock `json:"child_page"`
}

type richBlock struct {
	RichText []richText `json:"rich_text"`
	Title    []richText `json:"title"`
}

type todoBlock struct {
	RichText []richText `json:"rich_text"`
	Checked  bool       `json:"checked"`
}

type richText struct {
	PlainText string `json:"plain_text"`
}
