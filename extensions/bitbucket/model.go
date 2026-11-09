package main

type pullRequest struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Open        bool   `json:"open"`
	Author      struct {
		User user `json:"user"`
	} `json:"author"`
	Reviewers []struct {
		User     user   `json:"user"`
		Approved bool   `json:"approved"`
		Status   string `json:"status"`
	} `json:"reviewers"`
	FromRef struct {
		DisplayID  string `json:"displayId"`
		Repository struct {
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Project struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"project"`
		} `json:"repository"`
	} `json:"fromRef"`
	ToRef struct {
		DisplayID string `json:"displayId"`
	} `json:"toRef"`
	Links struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

type user struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

type activity struct {
	ID      int64  `json:"id"`
	Action  string `json:"action"`
	Comment struct {
		Text   string `json:"text"`
		Author user   `json:"author"`
	} `json:"comment"`
	User      user  `json:"user"`
	CreatedAt int64 `json:"createdDate"`
}

type prPage struct {
	Values        []pullRequest `json:"values"`
	Size          int           `json:"size"`
	Limit         int           `json:"limit"`
	Start         int           `json:"start"`
	NextPageStart *int          `json:"nextPageStart"`
	IsLastPage    bool          `json:"isLastPage"`
}
