package main

import "regexp"

type bookmark struct {
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	SiteName     string   `json:"site_name"`
	Site         string   `json:"site"`
	Authors      []string `json:"authors"`
	Description  string   `json:"description"`
	Note         string   `json:"note"`
	Labels       []string `json:"labels"`
	Loaded       bool     `json:"loaded"`
	HasArticle   bool     `json:"has_article"`
	IsMarked     bool     `json:"is_marked"`
	IsArchived   bool     `json:"is_archived"`
	IsDeleted    bool     `json:"is_deleted"`
	ReadProgress int      `json:"read_progress"`
	ReadingTime  int      `json:"reading_time"`
	WordCount    int      `json:"word_count"`
	Created      string   `json:"created"`
	Updated      string   `json:"updated"`
}

type highlight struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type bookmarkPage struct {
	Items    []bookmark
	Total    int
	HasMore  bool
	NextPage int
}

var bookmarkIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validBookmarkID(id string) bool { return bookmarkIDPattern.MatchString(id) }
