package main

import "encoding/json"

type article struct {
	ID             int64                           `json:"id"`
	Title          string                          `json:"title"`
	Description    string                          `json:"description"`
	URL            string                          `json:"url"`
	CommentsCount  int                             `json:"comments_count"`
	ReactionsCount int                             `json:"public_reactions_count"`
	PublishedAt    string                          `json:"published_at"`
	ReadingMinutes int                             `json:"reading_time_minutes"`
	TagList        json.RawMessage                 `json:"tag_list"`
	Tags           []string                        `json:"tags"`
	BodyMarkdown   string                          `json:"body_markdown"`
	User           struct{ Name, Username string } `json:"user"`
}

type comment struct {
	ID        string                `json:"id_code"`
	CreatedAt string                `json:"created_at"`
	BodyHTML  string                `json:"body_html"`
	User      struct{ Name string } `json:"user"`
	Children  []comment             `json:"children"`
}

type config struct {
	Tags         []string `json:"tags"`
	MinReactions int      `json:"min_reactions"`
}

func (a article) tagNames() []string {
	if len(a.Tags) > 0 {
		return a.Tags
	}
	var list []string
	if json.Unmarshal(a.TagList, &list) == nil {
		return list
	}
	return nil
}
