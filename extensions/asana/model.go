package main

type resource struct {
	GID          string    `json:"gid"`
	Name         string    `json:"name"`
	ResourceType string    `json:"resource_type"`
	Archived     bool      `json:"archived"`
	Workspace    *resource `json:"workspace"`
}

type project struct {
	GID       string    `json:"gid"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	Workspace *resource `json:"workspace"`
}

type workspace struct {
	GID            string `json:"gid"`
	Name           string `json:"name"`
	IsOrganization bool   `json:"is_organization"`
}

type task struct {
	GID             string     `json:"gid"`
	Name            string     `json:"name"`
	Notes           string     `json:"notes"`
	Completed       bool       `json:"completed"`
	CompletedAt     string     `json:"completed_at"`
	DueOn           string     `json:"due_on"`
	DueAt           string     `json:"due_at"`
	StartOn         string     `json:"start_on"`
	PermalinkURL    string     `json:"permalink_url"`
	ResourceSubtype string     `json:"resource_subtype"`
	Assignee        *resource  `json:"assignee"`
	Projects        []resource `json:"projects"`
	Workspace       *resource  `json:"workspace"`
}

type page[T any] struct {
	Data     []T `json:"data"`
	NextPage *struct {
		Offset string `json:"offset"`
		URI    string `json:"uri"`
	} `json:"next_page"`
}
