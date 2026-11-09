package main

import "encoding/json"

type issue struct {
	ID          string                          `json:"id"`
	Identifier  string                          `json:"identifier"`
	Title       string                          `json:"title"`
	Description string                          `json:"description"`
	Priority    int                             `json:"priority"`
	URL         string                          `json:"url"`
	DueDate     string                          `json:"dueDate"`
	CreatedAt   string                          `json:"createdAt"`
	UpdatedAt   string                          `json:"updatedAt"`
	State       struct{ ID, Name, Type string } `json:"state"`
	Team        struct{ ID, Key, Name string }  `json:"team"`
	Assignee    *user                           `json:"assignee"`
	Project     *struct{ ID, Name string }      `json:"project"`
	Comments    connection[comment]             `json:"comments"`
}

type user struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type comment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	User      *user  `json:"user"`
}

type team struct {
	ID     string            `json:"id"`
	Key    string            `json:"key"`
	Name   string            `json:"name"`
	States connection[state] `json:"states"`
}

type state struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

type connection[T any] struct {
	Nodes    []T `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

type viewer struct {
	ID             string            `json:"id"`
	AssignedIssues connection[issue] `json:"assignedIssues"`
}

type graphRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}
