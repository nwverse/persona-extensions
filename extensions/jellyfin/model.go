package main

import "regexp"

type user struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

type mediaItem struct {
	ID                string `json:"Id"`
	Name              string `json:"Name"`
	Type              string `json:"Type"`
	Overview          string `json:"Overview"`
	ProductionYear    int    `json:"ProductionYear"`
	RunTimeTicks      int64  `json:"RunTimeTicks"`
	SeriesName        string `json:"SeriesName"`
	SeasonName        string `json:"SeasonName"`
	IndexNumber       int    `json:"IndexNumber"`
	ParentIndexNumber int    `json:"ParentIndexNumber"`
	MediaType         string `json:"MediaType"`
	UserData          struct {
		PlaybackPositionTicks int64   `json:"PlaybackPositionTicks"`
		PlayedPercentage      float64 `json:"PlayedPercentage"`
		IsFavorite            bool    `json:"IsFavorite"`
		Played                bool    `json:"Played"`
	} `json:"UserData"`
}

type itemPage struct {
	Items            []mediaItem `json:"Items"`
	TotalRecordCount int         `json:"TotalRecordCount"`
	StartIndex       int         `json:"StartIndex"`
}

var mediaIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validMediaID(id string) bool { return mediaIDPattern.MatchString(id) }
