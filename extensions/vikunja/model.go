package main

type user struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type task struct {
	ID          int64     `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Done        bool      `json:"done"`
	DueDate     string    `json:"due_date"`
	ProjectID   int64     `json:"project_id"`
	Assignees   []user    `json:"assignees"`
	Comments    []comment `json:"comments"`
	Updated     string    `json:"updated"`
}

type comment struct {
	ID      int64  `json:"id"`
	Comment string `json:"comment"`
	Created string `json:"created"`
	Author  user   `json:"author"`
}

type project struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Identifier string `json:"identifier"`
	IsArchived bool   `json:"is_archived"`
	DeletedAt  string `json:"deleted_at"`
}

type taskPage struct {
	Items     []task
	HasMore   bool
	NextPage  int
	Total     int
	PageCount int
}
