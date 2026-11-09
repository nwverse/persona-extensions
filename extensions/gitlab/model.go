package main

type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type mergeRequest struct {
	ID             int64  `json:"id"`
	IID            int64  `json:"iid"`
	ProjectID      int64  `json:"project_id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	State          string `json:"state"`
	SourceBranch   string `json:"source_branch"`
	TargetBranch   string `json:"target_branch"`
	WebURL         string `json:"web_url"`
	UpdatedAt      string `json:"updated_at"`
	Author         user   `json:"author"`
	Assignees      []user `json:"assignees"`
	Reviewers      []user `json:"reviewers"`
	UserNotesCount int    `json:"user_notes_count"`
	References     struct {
		Full string `json:"full"`
	} `json:"references"`
}

type note struct {
	ID         int64  `json:"id"`
	Body       string `json:"body"`
	Author     user   `json:"author"`
	System     bool   `json:"system"`
	Resolvable bool   `json:"resolvable"`
	CreatedAt  string `json:"created_at"`
}

type mrPage struct {
	Items []mergeRequest
	Next  string
}
