package main

type story struct {
	ShortID          string    `json:"short_id"`
	CreatedAt        string    `json:"created_at"`
	Title            string    `json:"title"`
	URL              string    `json:"url"`
	Score            int       `json:"score"`
	CommentCount     int       `json:"comment_count"`
	Description      string    `json:"description"`
	DescriptionPlain string    `json:"description_plain"`
	Submitter        string    `json:"submitter_user"`
	Tags             []string  `json:"tags"`
	ShortIDURL       string    `json:"short_id_url"`
	CommentsURL      string    `json:"comments_url"`
	Comments         []comment `json:"comments"`
}

type comment struct {
	ShortID     string  `json:"short_id"`
	CreatedAt   string  `json:"created_at"`
	IsDeleted   bool    `json:"is_deleted"`
	IsModerated bool    `json:"is_moderated"`
	Score       int     `json:"score"`
	Parent      *string `json:"parent_comment"`
	Text        string  `json:"comment_plain"`
	Depth       int     `json:"depth"`
	User        string  `json:"commenting_user"`
	URL         string  `json:"url"`
}

type config struct {
	Tags        []string `json:"tags"`
	ExcludeTags []string `json:"exclude_tags"`
	MinScore    int      `json:"min_score"`
}
