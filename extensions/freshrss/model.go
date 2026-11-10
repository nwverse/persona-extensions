package main

import "regexp"

type article struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Author     string   `json:"author"`
	Published  int64    `json:"published"`
	Categories []string `json:"categories"`
	Summary    struct {
		Content string `json:"content"`
	} `json:"summary"`
	Content struct {
		Content string `json:"content"`
	} `json:"content"`
	Alternate []struct {
		Href string `json:"href"`
	} `json:"alternate"`
	Origin struct {
		StreamID string `json:"streamId"`
		Title    string `json:"title"`
		HTMLURL  string `json:"htmlUrl"`
	} `json:"origin"`
}

type articlePage struct {
	Items        []article `json:"items"`
	Continuation string    `json:"continuation"`
}

type subscription struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	HTMLURL string `json:"htmlUrl"`
}

var itemIDPattern = regexp.MustCompile(`^tag:google\.com,2005:reader/item/[0-9a-fA-F]{1,32}$`)

func validItemID(id string) bool { return itemIDPattern.MatchString(id) }
