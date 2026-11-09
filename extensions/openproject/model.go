package main

type link struct {
	Href  string `json:"href"`
	Title string `json:"title"`
}

type workPackage struct {
	ID          int64  `json:"id"`
	Subject     string `json:"subject"`
	Description struct {
		Raw  string `json:"raw"`
		HTML string `json:"html"`
	} `json:"description"`
	StartDate string `json:"startDate"`
	DueDate   string `json:"dueDate"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Links     struct {
		Self     link `json:"self"`
		Project  link `json:"project"`
		Status   link `json:"status"`
		Type     link `json:"type"`
		Assignee link `json:"assignee"`
	} `json:"_links"`
}

type collection[T any] struct {
	Embedded struct {
		Elements []T `json:"elements"`
	} `json:"_embedded"`
	Links struct {
		NextByOffset link `json:"nextByOffset"`
	} `json:"_links"`
	Total    int `json:"total"`
	Count    int `json:"count"`
	PageSize int `json:"pageSize"`
	Offset   int `json:"offset"`
}

type project struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Active     bool   `json:"active"`
	Links      struct {
		Self link `json:"self"`
	} `json:"_links"`
}

type workType struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	IsDefault   bool   `json:"isDefault"`
	IsMilestone bool   `json:"isMilestone"`
	Links       struct {
		Self link `json:"self"`
	} `json:"_links"`
}

type currentUser struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Login string `json:"login"`
	Links struct {
		Self link `json:"self"`
	} `json:"_links"`
}
