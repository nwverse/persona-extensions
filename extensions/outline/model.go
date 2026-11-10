package main

import "regexp"

type document struct {
	ID           string `json:"id"`
	CollectionID string `json:"collectionId"`
	Title        string `json:"title"`
	Text         string `json:"text"`
	Path         string `json:"path"`
	URLID        string `json:"urlId"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	PublishedAt  string `json:"publishedAt"`
	ArchivedAt   string `json:"archivedAt"`
	DeletedAt    string `json:"deletedAt"`
}

type searchResult struct {
	Context  string   `json:"context"`
	Ranking  float64  `json:"ranking"`
	Document document `json:"document"`
}

type collection struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
	ArchivedAt string `json:"archivedAt"`
	DeletedAt  string `json:"deletedAt"`
}

type pagination struct {
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	NextPath string `json:"nextPath"`
}

type envelope[T any] struct {
	OK         bool       `json:"ok"`
	Data       T          `json:"data"`
	Pagination pagination `json:"pagination"`
	Error      string     `json:"error"`
}

var (
	uuidPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	urlIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func validDocumentID(id string) bool {
	return uuidPattern.MatchString(id) || urlIDPattern.MatchString(id)
}
