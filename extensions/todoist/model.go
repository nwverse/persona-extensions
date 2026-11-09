package main

// Task IDs and project IDs are opaque strings in Todoist's current API, not numbers.
type task struct {
	ID          string    `json:"id"`
	Content     string    `json:"content"`
	Description string    `json:"description"`
	ProjectID   string    `json:"project_id"`
	SectionID   string    `json:"section_id"`
	ParentID    string    `json:"parent_id"`
	Labels      []string  `json:"labels"`
	Priority    int       `json:"priority"`
	Due         *dueDate  `json:"due"`
	Duration    *duration `json:"duration"`
	AddedAt     string    `json:"added_at"`
	CompletedAt string    `json:"completed_at"`
	ChildOrder  int       `json:"child_order"`
}

type dueDate struct {
	Date      string `json:"date"`
	String    string `json:"string"`
	Recurring bool   `json:"recurring"`
	Datetime  string `json:"datetime"`
	Timezone  string `json:"timezone"`
}

type duration struct {
	Amount int    `json:"amount"`
	Unit   string `json:"unit"`
}

type project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id"`
	Inbox    bool   `json:"inbox_project"`
}

type section struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type contextData struct {
	Projects map[string]project
	Sections map[string]section
	Labels   map[string]label
}

// taskPatch distinguishes omitted (leave unchanged) from explicitly supplied fields.
// The zero value changes nothing; validatePatch requires at least one supplied field.
type taskPatch struct {
	Content     *string   `json:"content,omitempty"`
	Description *string   `json:"description,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	DueString   *string   `json:"due_string,omitempty"`
	DueDate     *string   `json:"due_date,omitempty"`
	DueDatetime *string   `json:"due_datetime,omitempty"`
	Labels      *[]string `json:"labels,omitempty"`
	ProjectID   *string   `json:"project_id,omitempty"`
	SectionID   *string   `json:"section_id,omitempty"`
}

func (p taskPatch) empty() bool {
	return p.Content == nil && p.Description == nil && p.Priority == nil && p.DueString == nil && p.DueDate == nil &&
		p.DueDatetime == nil && p.Labels == nil && p.ProjectID == nil && p.SectionID == nil
}
