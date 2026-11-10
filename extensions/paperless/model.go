package main

type document struct {
	ID               int64   `json:"id"`
	Title            string  `json:"title"`
	Content          string  `json:"content"`
	Created          string  `json:"created"`
	Modified         string  `json:"modified"`
	Added            string  `json:"added"`
	OriginalFileName string  `json:"original_file_name"`
	MIMEType         string  `json:"mime_type"`
	PageCount        int     `json:"page_count"`
	Correspondent    *int64  `json:"correspondent"`
	DocumentType     *int64  `json:"document_type"`
	Tags             []int64 `json:"tags"`
	Notes            []note  `json:"notes"`
	SearchHit        struct {
		Highlights string  `json:"highlights"`
		Score      float64 `json:"score"`
		Rank       int     `json:"rank"`
	} `json:"__search_hit__"`
}

type note struct {
	ID      int64  `json:"id"`
	Note    string `json:"note"`
	Created string `json:"created"`
	User    struct {
		Username string `json:"username"`
	} `json:"user"`
}

type namedObject struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DocumentCount int    `json:"document_count"`
}

type page[T any] struct {
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Results  []T     `json:"results"`
}

type documentPage struct {
	Items    []document
	Count    int
	HasMore  bool
	NextPage int
}
