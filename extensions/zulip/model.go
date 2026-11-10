package main

import "encoding/json"

type stream struct {
	ID          int64  `json:"stream_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsArchived  bool   `json:"is_archived"`
}

type topic struct {
	Name  string `json:"name"`
	MaxID int64  `json:"max_id"`
}

type message struct {
	ID               int64           `json:"id"`
	Content          string          `json:"content"`
	ContentType      string          `json:"content_type"`
	SenderFullName   string          `json:"sender_full_name"`
	SenderID         int64           `json:"sender_id"`
	Timestamp        int64           `json:"timestamp"`
	StreamID         int64           `json:"stream_id"`
	Subject          string          `json:"subject"`
	DisplayRecipient json.RawMessage `json:"display_recipient"`
}

type messagePage struct {
	Messages    []message `json:"messages"`
	FoundOldest bool      `json:"found_oldest"`
	FoundNewest bool      `json:"found_newest"`
	FoundAnchor bool      `json:"found_anchor"`
}

type apiError struct {
	Result string `json:"result"`
	Msg    string `json:"msg"`
}
