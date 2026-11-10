package main

type timesheet struct {
	ID          int64   `json:"id"`
	Begin       string  `json:"begin"`
	End         *string `json:"end"`
	Duration    int     `json:"duration"`
	Description string  `json:"description"`
	Project     *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	Activity *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"activity"`
}

type namedResource struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Visible bool   `json:"visible"`
}
