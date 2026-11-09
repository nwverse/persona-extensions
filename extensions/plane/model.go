package main

import (
	"encoding/json"
	"strings"
)

type project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Identifier  string `json:"identifier"`
	Description string `json:"description"`
}

type workItem struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	DescriptionStripped string          `json:"description_stripped"`
	DescriptionHTML     string          `json:"description_html"`
	SequenceID          int64           `json:"sequence_id"`
	Priority            string          `json:"priority"`
	Project             string          `json:"project"`
	ProjectIdentifier   string          `json:"project__identifier"`
	State               entityRef       `json:"state"`
	TargetDate          string          `json:"target_date"`
	WebURL              string          `json:"web_url"`
	UpdatedAt           string          `json:"updated_at"`
	Assignees           json.RawMessage `json:"assignees"`
}

type entityRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

func (r *entityRef) UnmarshalJSON(data []byte) error {
	var id string
	if json.Unmarshal(data, &id) == nil {
		r.ID = id
		return nil
	}
	type alias entityRef
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*r = entityRef(v)
	return nil
}

type workflowState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Sequence int    `json:"sequence"`
}

type page[T any] struct {
	Results         []T    `json:"results"`
	NextCursor      string `json:"next_cursor"`
	NextPageResults bool   `json:"next_page_results"`
	TotalResults    int    `json:"total_results"`
}

func itemDescription(item workItem) string {
	if text := strings.TrimSpace(item.DescriptionStripped); text != "" {
		return text
	}
	return item.Description
}
